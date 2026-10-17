package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/riverqueue/river"
)

func TestClaimSQLShape(t *testing.T) {
	for _, want := range []string{"FOR UPDATE SKIP LOCKED", "LIMIT $1", "available_at <= now()", "RETURNING"} {
		if !strings.Contains(claimSQL, want) {
			t.Errorf("claimSQL lacks %q", want)
		}
	}
}

func TestAfterFailure(t *testing.T) {
	for attempts := 1; attempts < maxAttempts; attempts++ {
		full := min(baseDelay<<(attempts-1), maxDelay)
		dead, d := afterFailure(attempts)
		if dead || d < full/2 || d > full {
			t.Errorf("attempt %d: dead=%v delay=%v, want retry in [%v, %v]", attempts, dead, d, full/2, full)
		}
	}
	if dead, _ := afterFailure(maxAttempts); !dead {
		t.Errorf("attempt %d: want the dead state", maxAttempts)
	}
}

// The relay drains on its poll interval with no notification at all, drains
// early on a wake-up, and returns when ctx ends.
func TestRunRelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wake := make(chan struct{}, 1)
		var drains atomic.Int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			runRelay(ctx, wake, time.Second, func(context.Context) error { drains.Add(1); return nil })
		}()
		time.Sleep(3*time.Second + time.Millisecond) // no NOTIFY: polls at 1s, 2s, 3s
		synctest.Wait()
		if n := drains.Load(); n != 4 {
			t.Fatalf("drains after 3 polls = %d, want 4", n)
		}
		wake <- struct{}{}
		synctest.Wait()
		if n := drains.Load(); n != 5 {
			t.Fatalf("drains after wake-up = %d, want 5", n)
		}
		cancel()
		<-done
	})
}

func TestRiverConfigLeavesPoolForRequests(t *testing.T) {
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &SendReceiptWorker{}); err != nil {
		t.Fatal(err)
	}
	cfg := riverConfig(workers, poolMaxConns)
	total := 0
	for _, q := range cfg.Queues {
		total += q.MaxWorkers
	}
	if total == 0 || total >= poolMaxConns {
		t.Errorf("workers = %d, want 1..%d so requests keep connections", total, poolMaxConns-1)
	}
	if cfg.SoftStopTimeout <= 0 || cfg.SoftStopTimeout >= shutdownGrace {
		t.Errorf("SoftStopTimeout = %v, want below the %v grace period", cfg.SoftStopTimeout, shutdownGrace)
	}
}
