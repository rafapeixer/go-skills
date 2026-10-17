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
	// Parallel pricing calls per quote, and per process across all quotes.
	perQuoteConcurrency = 10
	pricingMaxInFlight  = 64
	// How long a call waits for a free slot before failing fast.
	bulkheadWait = 100 * time.Millisecond

	maxPriceBody = 4 << 10
	maxDrain     = 64 << 10
)

var (
	errUnknownSKU         = errors.New("unknown sku")
	errPricingUnavailable = errors.New("pricing service unavailable")
	errNotPriced          = fmt.Errorf("%w: item not priced", errPricingUnavailable)
)

type pricingClient struct {
	base string
	hc   *http.Client
	sem  chan struct{}
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
	}
}

// total prices every item concurrently and returns the order total. It
// returns errUnknownSKU if any SKU is unknown, and an error wrapping
// errPricingUnavailable if the service fails or does not answer in time.
func (p *pricingClient) total(ctx context.Context, items []Item) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, quoteTimeout)
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

func (p *pricingClient) price(ctx context.Context, sku string) (int64, error) {
	timer := time.NewTimer(bulkheadWait)
	defer timer.Stop()
	select {
	case p.sem <- struct{}{}:
	case <-timer.C:
		return 0, fmt.Errorf("%w: too many in-flight calls", errPricingUnavailable)
	case <-ctx.Done():
		return 0, fmt.Errorf("%w: %w", errPricingUnavailable, ctx.Err())
	}
	defer func() { <-p.sem }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/prices/"+url.PathEscape(sku), nil)
	if err != nil {
		return 0, fmt.Errorf("%w: build request: %w", errPricingUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errPricingUnavailable, err)
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
