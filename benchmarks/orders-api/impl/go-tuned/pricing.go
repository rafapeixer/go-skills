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
)

const (
	// Whole budget for pricing every item of one order.
	quoteTimeout = 2500 * time.Millisecond
	// Budget for one call. The service usually answers in ~20 ms; a call
	// slower than this counts as a failure so that a slow dependency opens
	// the breaker instead of holding goroutines for the whole quote budget.
	priceTimeout = time.Second
	// Parallel pricing calls per quote, and per process across all quotes.
	// By Little's law the process-wide limit must cover the expected call
	// rate times the call latency, with headroom: 8 replicas at 10,000 req/s
	// make about 4,500 calls/s, which at 30 ms is 135 in flight per replica.
	perQuoteConcurrency = 10
	pricingMaxInFlight  = 256

	// The breaker opens after this many consecutive failed calls and lets one
	// probe through after breakerOpenFor; the probe's result decides whether
	// it closes again or stays open for another period.
	breakerFailures = 5
	breakerOpenFor  = 2 * time.Second

	maxPriceBody = 4 << 10
	maxDrain     = 64 << 10
)

var (
	errUnknownSKU         = errors.New("unknown sku")
	errPricingUnavailable = errors.New("pricing service unavailable")
	errNotPriced          = fmt.Errorf("%w: item not priced", errPricingUnavailable)
	errPriceBudget        = errors.New("pricing call budget exceeded")
	errBreakerOpen        = fmt.Errorf("%w: circuit open", errPricingUnavailable)
	errBulkheadFull       = fmt.Errorf("%w: too many in-flight calls", errPricingUnavailable)
)

type pricingClient struct {
	base string
	hc   *http.Client
	sem  chan struct{}
	cb   breaker
}

func newPricingClient(base *url.URL) *pricingClient {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = pricingMaxInFlight
	return &pricingClient{
		base: strings.TrimRight(base.String(), "/"),
		hc: &http.Client{
			Transport: t,
			Timeout:   quoteTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		sem: make(chan struct{}, pricingMaxInFlight),
		cb:  breaker{failures: breakerFailures, openFor: breakerOpenFor},
	}
}

// total prices every item concurrently and returns the order total. It
// returns errUnknownSKU if any SKU is unknown, and an error wrapping
// errPricingUnavailable if the service fails or does not answer in time.
func (p *pricingClient) total(ctx context.Context, items []Item) (int64, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, quoteTimeout, errPriceBudget)
	defer cancel()

	prices := make([]int64, len(items))
	errs := make([]error, len(items))
	for i := range errs {
		errs[i] = errNotPriced
	}

	work := make(chan int)
	var wg sync.WaitGroup
	for range min(len(items), perQuoteConcurrency) {
		wg.Go(func() {
			for i := range work {
				prices[i], errs[i] = p.price(ctx, items[i].SKU)
				if errs[i] != nil {
					cancel() // the quote has failed; stop the remaining calls
				}
			}
		})
	}
feed:
	for i := range items {
		select {
		case work <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()

	for _, err := range errs {
		if errors.Is(err, errUnknownSKU) {
			return 0, err
		}
	}
	var total int64
	for i, err := range errs {
		if err != nil {
			return 0, err
		}
		total += prices[i] * int64(items[i].Qty)
	}
	return total, nil
}

// price looks up one SKU. The bulkhead and the breaker both fail fast: a
// full bulkhead or an open breaker costs the caller nothing but an error.
func (p *pricingClient) price(ctx context.Context, sku string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("%w: %w", errPricingUnavailable, context.Cause(ctx))
	}
	if !p.cb.allow() {
		return 0, errBreakerOpen
	}
	select {
	case p.sem <- struct{}{}:
	default:
		return 0, errBulkheadFull
	}
	defer func() { <-p.sem }()

	cents, err := p.fetch(ctx, sku)
	switch {
	case err == nil, errors.Is(err, errUnknownSKU):
		p.cb.record(true) // the service answered; 404 is an answer
	case ctx.Err() != nil:
		// The quote was cancelled or ran out of budget; that says nothing
		// about the pricing service.
	default:
		p.cb.record(false)
	}
	return cents, err
}

func (p *pricingClient) fetch(ctx context.Context, sku string) (int64, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, priceTimeout, errPriceBudget)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/prices/"+url.PathEscape(sku), nil)
	if err != nil {
		return 0, fmt.Errorf("%w: build request: %w", errPricingUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
		return 0, fmt.Errorf("%w: sku %q: %w", errPricingUnavailable, sku, err)
	}
	defer func() {
		// Drain on every path so the connection goes back to the pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return 0, fmt.Errorf("%w: %q", errUnknownSKU, sku)
	default:
		return 0, fmt.Errorf("%w: status %d for sku %q", errPricingUnavailable, resp.StatusCode, sku)
	}

	var body struct {
		Cents *int64 `json:"cents"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPriceBody)).Decode(&body); err != nil {
		return 0, fmt.Errorf("%w: decode price for sku %q: %w", errPricingUnavailable, sku, err)
	}
	if body.Cents == nil || *body.Cents < 0 {
		return 0, fmt.Errorf("%w: invalid price for sku %q", errPricingUnavailable, sku)
	}
	return *body.Cents, nil
}

// breaker is a circuit breaker: closed while calls succeed, open for openFor
// after `failures` consecutive failures, then half-open. Half-open admits one
// probe and re-arms the open period at the same time, so a probe whose
// result is never reported (its quote was cancelled) cannot wedge the
// breaker: the next probe is simply admitted one period later.
type breaker struct {
	failures int
	openFor  time.Duration

	mu          sync.Mutex
	consecutive int
	openUntil   time.Time // zero while closed
}

func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return true
	}
	now := time.Now()
	if now.Before(b.openUntil) {
		return false
	}
	b.openUntil = now.Add(b.openFor) // admit this probe; keep the others out
	return true
}

func (b *breaker) record(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.consecutive = 0
		b.openUntil = time.Time{}
		return
	}
	b.consecutive++
	// A failure while open (a probe, or a call admitted just before the
	// breaker opened) extends the open period; otherwise count toward it.
	if !b.openUntil.IsZero() || b.consecutive >= b.failures {
		b.consecutive = 0
		b.openUntil = time.Now().Add(b.openFor)
	}
}
