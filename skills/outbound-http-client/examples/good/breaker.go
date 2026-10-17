package upstream

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrOpen = errors.New("upstream: circuit open")

// Breaker makes calls to a slow or failing upstream fail fast. Each call gets
// its own timeout near the upstream's normal latency; after `failures`
// consecutive failures the breaker opens, and after openFor it admits a single
// probe whose result closes it again or keeps it open for another period.
type Breaker struct {
	failures int
	openFor  time.Duration
	timeout  time.Duration

	mu          sync.Mutex
	consecutive int
	openUntil   time.Time // zero while closed
}

func NewBreaker(failures int, openFor, callTimeout time.Duration) *Breaker {
	return &Breaker{failures: failures, openFor: openFor, timeout: callTimeout}
}

// Do runs call unless the breaker is open. A call the caller cancelled says
// nothing about the upstream, so it is not counted either way.
func (b *Breaker) Do(ctx context.Context, call func(context.Context) error) error {
	if !b.allow() {
		return ErrOpen
	}
	callCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	err := call(callCtx)
	if ctx.Err() == nil {
		b.record(err == nil)
	}
	return err
}

func (b *Breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return true
	}
	now := time.Now()
	if now.Before(b.openUntil) {
		return false
	}
	// Half-open: admit this probe and re-arm the open period, so every other
	// call keeps failing fast until the probe reports.
	b.openUntil = now.Add(b.openFor)
	return true
}

func (b *Breaker) record(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.consecutive = 0
		b.openUntil = time.Time{}
		return
	}
	b.consecutive++
	if !b.openUntil.IsZero() || b.consecutive >= b.failures {
		b.consecutive = 0
		b.openUntil = time.Now().Add(b.openFor)
	}
}
