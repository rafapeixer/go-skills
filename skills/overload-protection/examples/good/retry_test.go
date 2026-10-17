package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

// failing returns an op that always fails transiently and counts its calls.
func failing(calls *int) func(context.Context) error {
	return func(context.Context) error {
		*calls++
		return fmt.Errorf("pricing: 503: %w", errTransient)
	}
}

func TestRetryStopsAtBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBudget(0.1, 2) // two retries banked, then 0.1 per call
		var first, second int
		start := time.Now()
		if err := retry(t.Context(), b, true, failing(&first)); !errors.Is(err, errTransient) {
			t.Fatalf("err = %v, want errTransient", err)
		}
		if first != maxAttempts {
			t.Errorf("first call made %d attempts, want %d", first, maxAttempts)
		}
		if waited := time.Since(start); waited >= baseDelay+2*baseDelay {
			t.Errorf("backoff took %v, want under %v (full jitter, capped per attempt)", waited, 3*baseDelay)
		}
		retry(t.Context(), b, true, failing(&second))
		if second != 1 {
			t.Errorf("call after the budget is spent made %d attempts, want 1", second)
		}
	})
}

func TestRetrySkipsNonIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		retry(t.Context(), newBudget(0.1, 10), false, failing(&calls))
		if calls != 1 {
			t.Errorf("non-idempotent op ran %d times, want 1", calls)
		}
	})
}

func TestRetrySkipsPermanentErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		op := func(context.Context) error { calls++; return errors.New("400 bad sku") }
		retry(t.Context(), newBudget(0.1, 10), true, op)
		if calls != 1 {
			t.Errorf("permanent error retried: %d calls, want 1", calls)
		}
	})
}

func TestRetryStopsAtDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		var calls int
		slow := func(context.Context) error { // a slow upstream eats the whole deadline
			calls++
			time.Sleep(time.Second)
			return errTransient
		}
		retry(ctx, newBudget(0.1, 10), true, slow)
		if calls != 1 {
			t.Errorf("retried past the deadline: %d calls, want 1", calls)
		}
	})
}
