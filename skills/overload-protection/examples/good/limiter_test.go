package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestShedRejectsExcessImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		busy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release })
		h := shed(newLimiter(2, 1, 2, time.Second), time.Second, busy)

		var wg sync.WaitGroup
		for range 2 { // fill both slots with requests that stay in flight
			wg.Go(func() { h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)) })
		}
		synctest.Wait()

		start := time.Now()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("third request = %d, want 503", rec.Code)
		}
		if got := rec.Header().Get("Retry-After"); got != "1" {
			t.Errorf("Retry-After = %q, want \"1\"", got)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("shed request waited %v on the fake clock, want 0 (no queueing)", waited)
		}
		close(release)
		wg.Wait()
	})
}

func TestLimiterAIMD(t *testing.T) {
	l := newLimiter(10, 2, 20, 100*time.Millisecond)
	for range 10 {
		l.tryAcquire()
		l.release(10*time.Millisecond, false) // fast: grows by 1/limit each
	}
	if got := l.current(); got != 10 {
		t.Fatalf("limit after 10 fast requests = %d, want 10 (additive growth is slow)", got)
	}
	l.tryAcquire()
	l.release(time.Second, false) // slow: cut by 10%
	if got := l.current(); got != 9 {
		t.Errorf("limit after a slow request = %d, want 9", got)
	}
}

func TestBulkheadFailsFast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBulkhead(1)
		hold := make(chan struct{})
		go b.do(t.Context(), func(context.Context) error { <-hold; return nil })
		synctest.Wait()

		start := time.Now()
		if err := b.do(t.Context(), func(context.Context) error { return nil }); !errors.Is(err, errBulkheadFull) {
			t.Errorf("second call = %v, want errBulkheadFull", err)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("full bulkhead waited %v, want 0", waited)
		}
		close(hold)
	})
}
