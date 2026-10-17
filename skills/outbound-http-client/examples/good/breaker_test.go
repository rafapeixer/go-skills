package upstream

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

var errUpstream = errors.New("upstream: status 503")

// A slow upstream costs one call timeout per call until the breaker opens;
// after that, calls fail at once without reaching it.
func TestBreakerOpensAfterFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := NewBreaker(3, 2*time.Second, 50*time.Millisecond)
		calls := 0
		slow := func(ctx context.Context) error { calls++; <-ctx.Done(); return ctx.Err() }
		for range 3 {
			start := time.Now()
			if err := b.Do(t.Context(), slow); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("slow call = %v, want DeadlineExceeded", err)
			}
			if waited := time.Since(start); waited != 50*time.Millisecond {
				t.Errorf("slow call took %v, want the 50ms call timeout", waited)
			}
		}
		start := time.Now()
		if err := b.Do(t.Context(), slow); !errors.Is(err, ErrOpen) {
			t.Fatalf("fourth call = %v, want ErrOpen", err)
		}
		if waited := time.Since(start); waited != 0 || calls != 3 {
			t.Errorf("open breaker waited %v and made %d calls, want 0 and 3", waited, calls)
		}
	})
}

// After openFor one probe goes through; a successful probe closes the breaker.
func TestBreakerRecoversAfterProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := NewBreaker(1, 2*time.Second, 50*time.Millisecond)
		fail := func(context.Context) error { return errUpstream }
		ok := func(context.Context) error { return nil }
		_ = b.Do(t.Context(), fail)
		if err := b.Do(t.Context(), ok); !errors.Is(err, ErrOpen) {
			t.Fatalf("call while open = %v, want ErrOpen", err)
		}
		time.Sleep(2 * time.Second)
		if err := b.Do(t.Context(), fail); !errors.Is(err, errUpstream) {
			t.Fatalf("failed probe = %v, want errUpstream", err)
		}
		if err := b.Do(t.Context(), ok); !errors.Is(err, ErrOpen) {
			t.Fatalf("call after a failed probe = %v, want ErrOpen", err)
		}
		time.Sleep(2 * time.Second)
		for i := range 3 {
			if err := b.Do(t.Context(), ok); err != nil {
				t.Fatalf("call %d after a good probe = %v, want nil", i, err)
			}
		}
	})
}

// A call the caller cancelled is not the upstream's fault and is not counted.
func TestBreakerIgnoresCallerCancel(t *testing.T) {
	b := NewBreaker(1, 2*time.Second, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = b.Do(ctx, func(ctx context.Context) error { return ctx.Err() })
	if err := b.Do(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Errorf("call after a cancelled one = %v, want nil (breaker closed)", err)
	}
}
