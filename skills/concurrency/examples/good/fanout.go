package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// maxDBConns stands in for the pgxpool MaxConns. Fan-out that queries the
// database never runs more queries at once than the pool can serve.
const maxDBConns = 4

var ErrNotFound = errors.New("not found")

type Price struct {
	SKU   string
	Cents int
}

type lookupFunc func(ctx context.Context, sku string) (Price, error)

// prices looks up every SKU concurrently. The first failure cancels ctx for
// the lookups still running, and Wait returns only after all of them exit.
func prices(ctx context.Context, skus []string, lookup lookupFunc) ([]Price, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(maxDBConns)
	out := make([]Price, len(skus))
	for i, sku := range skus {
		g.Go(func() error {
			p, err := lookup(ctx, sku)
			if err != nil {
				return fmt.Errorf("price %q: %w", sku, err)
			}
			out[i] = p // each goroutine writes its own index: no lock needed
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// cache is state shared by many goroutines, so a mutex guards it.
type cache struct {
	mu sync.Mutex
	m  map[string]Price
}

func (c *cache) get(sku string) (Price, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.m[sku]
	return p, ok
}

func (c *cache) put(p Price) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[p.SKU] = p
}

// lookup stands in for a database query that honours ctx.
func (c *cache) lookup(ctx context.Context, sku string) (Price, error) {
	if p, ok := c.get(sku); ok {
		return p, nil
	}
	select {
	case <-ctx.Done():
		return Price{}, context.Cause(ctx)
	case <-time.After(10 * time.Millisecond):
	}
	if sku == "" {
		return Price{}, ErrNotFound
	}
	p := Price{SKU: sku, Cents: 100 * len(sku)}
	c.put(p)
	return p, nil
}
