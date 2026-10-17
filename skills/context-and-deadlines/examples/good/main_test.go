package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

func newServer(delay time.Duration) (*server, *memAudit) {
	audit := &memAudit{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &server{prices: fakePrices{delay: delay}, audit: audit, logger: logger}, audit
}

func TestPriceStopsAtItsBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newServer(10 * time.Second)
		start := time.Now()
		_, err := s.quote(t.Context(), []string{"a"})
		if !errors.Is(err, errPriceBudget) {
			t.Fatalf("err = %v, want cause errPriceBudget", err)
		}
		if got := time.Since(start); got != 500*time.Millisecond {
			t.Errorf("returned after %v, want 500ms", got)
		}
	})
}

func TestQuoteStopsAtRequestBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newServer(400 * time.Millisecond) // each call fits its own budget
		ctx, cancel := context.WithTimeoutCause(t.Context(), time.Second, errRequestBudget)
		defer cancel()
		_, err := s.quote(ctx, []string{"a", "b", "c", "d"})
		if !errors.Is(err, errRequestBudget) {
			t.Fatalf("err = %v, want cause errRequestBudget", err)
		}
	})
}

func TestAuditOutlivesResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, audit := newServer(10 * time.Millisecond)
		h := withDeadline(2*time.Second, http.HandlerFunc(s.handleQuote))
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/quote?sku=a", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		s.wg.Wait()
		if audit.count() != 1 {
			t.Errorf("audit records = %d, want 1; the write was cancelled with the request", audit.count())
		}
	})
}

func TestReadLineStopsOnCancel(t *testing.T) {
	errShutdown := errors.New("shutting down")
	ctx, cancel := context.WithCancelCause(t.Context())
	client, srv := net.Pipe()
	defer client.Close()
	defer srv.Close()

	cancel(errShutdown) // the peer never writes; only cancellation can unblock the read
	if _, err := readLine(ctx, client); !errors.Is(err, errShutdown) {
		t.Fatalf("err = %v, want errShutdown", err)
	}
}
