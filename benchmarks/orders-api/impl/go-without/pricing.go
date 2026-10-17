package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// Pricing normally answers in ~20 ms; a single lookup that takes longer
	// than this is treated as a failure rather than tying up the request.
	priceCallTimeout = 1 * time.Second
	// Upper bound for a whole quote, including waiting for concurrency slots.
	quoteTimeout = 3 * time.Second
	// Parallel lookups per quote.
	quoteConcurrency = 10
	// In-flight pricing calls per process. This is a bulkhead: when pricing is
	// slow, quotes fail fast with 503 instead of piling up goroutines and
	// sockets on a 1 GiB instance.
	maxInflightPriceCalls = 64
)

var (
	errUnknownSKU         = errors.New("unknown sku")
	errPricingUnavailable = errors.New("pricing service unavailable")
)

type PricingClient struct {
	baseURL string
	http    *http.Client
	slots   chan struct{}
}

func NewPricingClient(baseURL string) *PricingClient {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   500 * time.Millisecond,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxInflightPriceCalls,
		MaxIdleConnsPerHost:   maxInflightPriceCalls,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   1 * time.Second,
		ResponseHeaderTimeout: priceCallTimeout,
	}
	return &PricingClient{
		baseURL: baseURL,
		http:    &http.Client{Transport: transport, Timeout: priceCallTimeout},
		slots:   make(chan struct{}, maxInflightPriceCalls),
	}
}

// Price returns the unit price of sku in cents. Errors wrap errUnknownSKU or
// errPricingUnavailable.
func (p *PricingClient) Price(ctx context.Context, sku string) (int64, error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return 0, fmt.Errorf("%w: %v", errPricingUnavailable, ctx.Err())
	}
	defer func() { <-p.slots }()

	ctx, cancel := context.WithTimeout(ctx, priceCallTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/prices/"+url.PathEscape(sku), nil)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errPricingUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errPricingUnavailable, err)
	}
	defer func() {
		// Drain so the connection can be reused.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			Cents *int64 `json:"cents"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
			return 0, fmt.Errorf("%w: decode response for %q: %v", errPricingUnavailable, sku, err)
		}
		if body.Cents == nil || *body.Cents < 0 {
			return 0, fmt.Errorf("%w: invalid price for %q", errPricingUnavailable, sku)
		}
		return *body.Cents, nil
	case http.StatusNotFound:
		return 0, fmt.Errorf("%w: %q", errUnknownSKU, sku)
	default:
		return 0, fmt.Errorf("%w: status %d for %q", errPricingUnavailable, resp.StatusCode, sku)
	}
}

// Quote prices all items concurrently and returns the total in cents. The
// first failure cancels the remaining lookups.
func (p *PricingClient) Quote(ctx context.Context, items []Item) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, quoteTimeout)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		total    int64
		firstErr error
		local    = make(chan struct{}, quoteConcurrency)
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}

	for _, it := range items {
		wg.Go(func() {
			select {
			case local <- struct{}{}:
			case <-ctx.Done():
				fail(fmt.Errorf("%w: %v", errPricingUnavailable, ctx.Err()))
				return
			}
			defer func() { <-local }()

			cents, err := p.Price(ctx, it.SKU)
			if err != nil {
				fail(err)
				return
			}
			mu.Lock()
			total += cents * int64(it.Qty)
			mu.Unlock()
		})
	}
	wg.Wait()

	if firstErr != nil {
		return 0, firstErr
	}
	return total, nil
}
