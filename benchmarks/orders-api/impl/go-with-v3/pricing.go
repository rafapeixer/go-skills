package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

var (
	errUnknownSKU         = errors.New("unknown sku")
	errPricingUnavailable = errors.New("pricing service unavailable")
	errBreakerOpen        = errors.New("circuit breaker open")
)

const (
	// The pricing service usually answers in ~20 ms; one call may take 1s
	// before it counts as a failure, and a whole quote 2.5s.
	pricingCallTimeout = time.Second
	quoteTimeout       = 2500 * time.Millisecond
	pricingClientLimit = 3 * time.Second // backstop for any call without a deadline

	// Concurrent price lookups per quote: 100 items at ~20 ms take ~125 ms.
	quoteFanout = 16

	// pricingSlots sizes the pricing bulkhead by Little's law: a replica
	// admitting ~4,000 pricing calls/s at ~25 ms holds ~100 in flight; 256
	// gives 2.5x headroom for bursts and latency jitter.
	pricingSlots = 256

	breakerThreshold = 5
	breakerOpenFor   = 2 * time.Second

	maxPriceBody = 64 << 10
	maxDrain     = 64 << 10
)

type pricingClient struct {
	base    string
	hc      *http.Client
	slots   chan struct{}
	breaker *breaker
}

func newPricingClient(rawBase string) (*pricingClient, error) {
	u, err := url.Parse(rawBase)
	if err != nil {
		return nil, fmt.Errorf("parse PRICING_URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" {
		return nil, fmt.Errorf("PRICING_URL must be an http(s) base URL without query, got %q", rawBase)
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = pricingSlots
	return &pricingClient{
		base: strings.TrimSuffix(u.String(), "/"),
		hc: &http.Client{
			Transport: t,
			Timeout:   pricingClientLimit,
			// The pricing API does not redirect; never follow one elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		slots:   make(chan struct{}, pricingSlots),
		breaker: newBreaker(breakerThreshold, breakerOpenFor, pricingCallTimeout),
	}, nil
}

// quote returns the total price of items in cents.
func (p *pricingClient) quote(ctx context.Context, items []item) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, quoteTimeout)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(quoteFanout)
	lineTotals := make([]int64, len(items))
	for i, it := range items {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			cents, err := p.price(gctx, it.SKU)
			if err != nil {
				return err
			}
			lineTotals[i] = cents * int64(it.Qty)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		if errors.Is(err, errUnknownSKU) {
			return 0, err
		}
		return 0, fmt.Errorf("%w: %w", errPricingUnavailable, err)
	}
	var total int64
	for _, t := range lineTotals {
		total += t
	}
	return total, nil
}

// price looks up one SKU. It fails fast when the bulkhead is full or the
// breaker is open, so a slow pricing service cannot hold this replica.
func (p *pricingClient) price(ctx context.Context, sku string) (int64, error) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return 0, errors.New("pricing bulkhead full")
	}

	var (
		cents int64
		found bool
	)
	err := p.breaker.do(ctx, func(ctx context.Context) error {
		var err error
		cents, found, err = p.fetch(ctx, sku)
		return err
	})
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("%w: %s", errUnknownSKU, sku)
	}
	return cents, nil
}

// fetch returns found=false with a nil error for a 404: that is an answer,
// not a failure of the pricing service.
func (p *pricingClient) fetch(ctx context.Context, sku string) (cents int64, found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/prices/"+url.PathEscape(sku), nil)
	if err != nil {
		return 0, false, fmt.Errorf("build pricing request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("get price of %s: %w", sku, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("get price of %s: status %d", sku, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPriceBody+1))
	if err != nil {
		return 0, false, fmt.Errorf("read price of %s: %w", sku, err)
	}
	if len(body) > maxPriceBody {
		return 0, false, fmt.Errorf("price of %s: response larger than %d bytes", sku, maxPriceBody)
	}
	var pr struct {
		Cents *int64 `json:"cents"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, false, fmt.Errorf("decode price of %s: %w", sku, err)
	}
	if pr.Cents == nil || *pr.Cents < 0 {
		return 0, false, fmt.Errorf("price of %s: missing or negative cents", sku)
	}
	return *pr.Cents, true, nil
}

// breaker opens after threshold consecutive failures and fails calls fast
// for openFor; then it lets one probe through and re-arms the open period,
// so a probe that hangs cannot wedge it.
type breaker struct {
	threshold int
	openFor   time.Duration
	timeout   time.Duration

	mu        sync.Mutex
	failures  int
	openUntil time.Time
}

func newBreaker(threshold int, openFor, timeout time.Duration) *breaker {
	return &breaker{threshold: threshold, openFor: openFor, timeout: timeout}
}

func (b *breaker) do(ctx context.Context, call func(context.Context) error) error {
	if !b.allow() {
		return errBreakerOpen
	}
	callCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	err := call(callCtx)
	// A call the caller abandoned says nothing about the upstream.
	if ctx.Err() == nil {
		b.record(err == nil)
	}
	return err
}

func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < b.threshold {
		return true
	}
	now := time.Now()
	if now.Before(b.openUntil) {
		return false
	}
	b.openUntil = now.Add(b.openFor) // admit exactly one half-open probe
	return true
}

func (b *breaker) record(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures == b.threshold {
		b.openUntil = time.Now().Add(b.openFor)
	}
}
