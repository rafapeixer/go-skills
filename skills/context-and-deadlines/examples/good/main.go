// Command good gives every request a deadline with a cause, derives outbound
// budgets from it, and runs the audit after the response on its own timeout.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"
)

var (
	errRequestBudget = errors.New("request budget of 2s exceeded")
	errPriceBudget   = errors.New("price lookup budget of 500ms exceeded")
)

// prices is the outbound dependency; it takes ctx and stops when ctx ends.
type prices interface {
	Price(ctx context.Context, sku string) (int, error)
}

type auditLog interface {
	Record(ctx context.Context, skus []string)
}

type server struct {
	prices prices
	audit  auditLog
	logger *slog.Logger
	wg     sync.WaitGroup // owns post-response work; waited on at shutdown
}

// withDeadline gives every request a budget; everything below derives from it.
func withDeadline(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeoutCause(r.Context(), d, errRequestBudget)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *server) quote(ctx context.Context, skus []string) (int, error) {
	total := 0
	for _, sku := range skus {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("quote before %s: %w", sku, context.Cause(ctx))
		}
		p, err := s.price(ctx, sku)
		if err != nil {
			return 0, err
		}
		total += p
	}
	return total, nil
}

func (s *server) price(ctx context.Context, sku string) (int, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, 500*time.Millisecond, errPriceBudget)
	defer cancel()
	p, err := s.prices.Price(ctx, sku)
	if err != nil {
		return 0, fmt.Errorf("price %s: %w", sku, err)
	}
	return p, nil
}

func (s *server) handleQuote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	skus := r.URL.Query()["sku"]
	total, err := s.quote(ctx, skus)
	if err != nil {
		s.logger.WarnContext(ctx, "quote", "err", err)
		http.Error(w, "upstream timeout", http.StatusGatewayTimeout)
		return
	}
	fmt.Fprintf(w, "%d\n", total)

	// The request context is cancelled when this handler returns; the audit
	// keeps its values but not its cancellation, and gets its own budget.
	s.wg.Go(func() {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		s.audit.Record(actx, skus)
	})
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &server{prices: fakePrices{delay: 10 * time.Millisecond}, audit: &memAudit{}, logger: logger}
	h := withDeadline(2*time.Second, http.HandlerFunc(s.handleQuote))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/quote?sku=a&sku=b", nil))
	s.wg.Wait()
	logger.Info("quote", "status", rec.Code, "body", rec.Body.String())
}
