package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	maxAttempts = 3
	baseDelay   = 100 * time.Millisecond
	maxDelay    = 2 * time.Second
)

// errTransient marks failures worth retrying: 502, 503, 504, a refused or
// reset connection. The client that talks to the dependency wraps them.
var errTransient = errors.New("transient upstream failure")

// budget is a token bucket shared by every call one client makes. Each call
// deposits ratio tokens and each retry spends one, so retries stay near
// ratio of traffic however long the dependency keeps failing.
type budget struct {
	mu            sync.Mutex
	tokens, limit float64
	ratio         float64
}

func newBudget(ratio, limit float64) *budget {
	return &budget{tokens: limit, limit: limit, ratio: ratio}
}

func (b *budget) deposit() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens = min(b.limit, b.tokens+b.ratio)
}

func (b *budget) trySpend() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// retry runs op up to maxAttempts times. It retries only idempotent
// operations, only transient errors, only while the budget has tokens and
// only when the caller's deadline leaves room for the next wait.
func retry(ctx context.Context, b *budget, idempotent bool, op func(context.Context) error) error {
	b.deposit()
	var err error
	for attempt := range maxAttempts {
		if err = op(ctx); err == nil || !idempotent || !errors.Is(err, errTransient) {
			return err
		}
		if attempt == maxAttempts-1 || !b.trySpend() {
			return err
		}
		// Full jitter: a uniform wait in [0, min(maxDelay, baseDelay*2^attempt)).
		wait := rand.N(min(maxDelay, baseDelay<<attempt))
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= wait {
			return err // the retry could not finish in time anyway
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, context.Cause(ctx))
		case <-t.C:
		}
	}
	return err
}
