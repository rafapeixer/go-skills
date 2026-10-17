package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// countingOpen wraps openStore and counts how many times the store is built.
func countingOpen(calls *atomic.Int32, err error) func(string) (*store, error) {
	return func(dsn string) (*store, error) {
		calls.Add(1)
		if err != nil {
			return nil, err
		}
		return openStore(dsn)
	}
}

func get(h http.Handler, target string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	return rec.Code
}

func TestStartupBuildsNothing(t *testing.T) {
	var calls atomic.Int32
	a := newApp("postgres://db.internal/app", countingOpen(&calls, nil))
	h := a.routes()
	if code := get(h, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", code)
	}
	a.close()
	if n := calls.Load(); n != 0 {
		t.Fatalf("store built %d times before any request needed it, want 0", n)
	}
}

func TestStoreBuiltOnceUnderConcurrency(t *testing.T) {
	var calls atomic.Int32
	h := newApp("postgres://db.internal/app", countingOpen(&calls, nil)).routes()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if code := get(h, "/items/1"); code != http.StatusOK {
				t.Errorf("/items/1 = %d, want 200", code)
			}
		})
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("store built %d times, want 1", n)
	}
}

func TestBadConfigFailsRequestsNotStartup(t *testing.T) {
	var calls atomic.Int32
	h := newApp("", countingOpen(&calls, errors.New("no host"))).routes()
	if code := get(h, "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 even with a broken store", code)
	}
	if code := get(h, "/items/1"); code != http.StatusServiceUnavailable {
		t.Errorf("/items/1 = %d, want 503", code)
	}
}
