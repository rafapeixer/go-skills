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

const (
	// pricingSlots sizes the pricing bulkhead by Little's law: assuming up to
	// ~1,000 pricing calls/s per replica at ~25 ms each, 25 calls are in flight
	// on average; 64 leaves 2.5x headroom for bursts and tail latency. Redo the
	// arithmetic when real traffic numbers are known.
	pricingSlots = 64

	// perQuoteConcurrency caps how many bulkhead slots one quote can hold, so
	// a 100-item order cannot crowd out every other quote on the replica.
	perQuoteConcurrency = 8

	// pricingCallTimeout is near the service's ~20 ms normal latency with
	// margin for its tail; anything slower fails fast and counts against the
	// breaker instead of holding the request.
	pricingCallTimeout = time.Second

	breakerThreshold = 5
	breakerOpenFor   = 2 * time.Second

	maxPriceBody = 4 << 10
	maxDrain     = 64 << 10
)

var (
	errUnknownSKU         = errors.New("unknown sku")
	errPricingUnavailable = errors.New("pricing service unavailable")
	errBreakerOpen        = errors.New("circuit breaker open")
)

// limiter is a semaphore whose acquire never waits.
type limiter chan struct{}

func (l limiter) tryAcquire() bool {
	select {
	case l <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l limiter) release() { <-l }

type pricingClient struct {
	base    *url.URL
	hc      *http.Client
	breaker *breaker
	slots   limiter
}

func newPricingClient(base *url.URL) *pricingClient {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = pricingSlots // default is 2; match the bulkhead
	return &pricingClient{
		base: base,
		hc: &http.Client{
			Transport: t,
			Timeout:   2 * time.Second, // backstop; calls are bounded by pricingCallTimeout
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse // never follow redirects off the pricing host
			},
		},
		breaker: &breaker{threshold: breakerThreshold, openFor: breakerOpenFor, callTimeout: pricingCallTimeout},
		slots:   make(limiter, pricingSlots),
	}
}

// Total prices every item and returns the order total in cents. It returns an
// error wrapping errUnknownSKU or errPricingUnavailable.
func (c *pricingClient) Total(ctx context.Context, items []item) (int64, error) {
	prices := make([]int64, len(items))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(perQuoteConcurrency)
	for i, it := range items {
		if gctx.Err() != nil {
			break // a call already failed; don't start the rest
		}
		g.Go(func() error {
			p, err := c.Price(gctx, it.SKU)
			prices[i] = p
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("%w: %w", errPricingUnavailable, err)
	}
	var total int64
	for i, it := range items {
		total += prices[i] * it.Qty
	}
	return total, nil
}

func (c *pricingClient) Price(ctx context.Context, sku string) (int64, error) {
	if !c.slots.tryAcquire() {
		return 0, fmt.Errorf("%w: too many concurrent calls", errPricingUnavailable)
	}
	defer c.slots.release()

	var (
		cents    int64
		notFound bool
	)
	err := c.breaker.Do(ctx, func(ctx context.Context) error {
		var err error
		cents, notFound, err = c.fetch(ctx, sku)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("%w: price %q: %w", errPricingUnavailable, sku, err)
	}
	if notFound {
		return 0, fmt.Errorf("%w: %s", errUnknownSKU, sku)
	}
	return cents, nil
}

// fetch reports a 404 as notFound with a nil error: it is an answer, not a
// failure of the pricing service, and must not trip the breaker.
func (c *pricingClient) fetch(ctx context.Context, sku string) (cents int64, notFound bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.priceURL(sku), nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer func() {
		// Drain on every path so the connection goes back to the pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return 0, true, nil
	default:
		return 0, false, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var body struct {
		Cents *int64 `json:"cents"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPriceBody)).Decode(&body); err != nil {
		return 0, false, fmt.Errorf("decode response: %w", err)
	}
	if body.Cents == nil || *body.Cents < 0 {
		return 0, false, errors.New("response has no valid cents")
	}
	return *body.Cents, false, nil
}

// priceURL builds {base}/prices/{sku} with the SKU escaped as one path segment.
func (c *pricingClient) priceURL(sku string) string {
	u := *c.base
	u.Path = strings.TrimSuffix(c.base.Path, "/") + "/prices/" + sku
	u.RawPath = strings.TrimSuffix(c.base.EscapedPath(), "/") + "/prices/" + url.PathEscape(sku)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// breaker opens after threshold consecutive failures and fails calls at once
// for openFor. After that it admits one probe and re-arms the open period in
// the same step, so other callers keep failing fast while the probe runs and
// a lost probe cannot wedge it; a successful call closes it.
type breaker struct {
	threshold   int
	openFor     time.Duration
	callTimeout time.Duration

	mu        sync.Mutex
	failures  int
	openUntil time.Time
}

func (b *breaker) Do(ctx context.Context, call func(context.Context) error) error {
	if !b.allow() {
		return errBreakerOpen
	}
	callCtx, cancel := context.WithTimeout(ctx, b.callTimeout)
	defer cancel()
	err := call(callCtx)
	if ctx.Err() == nil { // a caller that gave up says nothing about the upstream
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
	b.openUntil = now.Add(b.openFor)
	return true
}

func (b *breaker) record(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.failures = 0
		b.openUntil = time.Time{}
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = time.Now().Add(b.openFor)
	}
}
