// Command good protects an HTTP service above capacity: an AIMD limiter
// sheds excess requests with 503 + Retry-After, the one outbound dependency
// sits behind a fail-fast bulkhead, and retries happen at that single call
// site, only for idempotent reads, within a 10% budget.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

const retryAfter = time.Second

// pricing is the outbound dependency; a real one wraps an *http.Client and
// returns errTransient for 502, 503, 504 and connection errors.
type pricing func(ctx context.Context, sku string) (int, error)

type app struct {
	price   pricing
	pool    *bulkhead // one bulkhead per dependency, sized below its capacity
	budget  *budget   // one retry budget per outbound client
	limiter *limiter
}

func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /price/{sku}", a.getPrice)
	return shed(a.limiter, retryAfter, mux)
}

func (a *app) getPrice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context() // carries the request deadline set at the edge
	if ctx.Err() != nil {
		return // the client is gone; do not spend the dependency on it
	}
	var cents int
	err := a.pool.do(ctx, func(ctx context.Context) error {
		// The only retry layer: a GET on a read-only endpoint is idempotent.
		return retry(ctx, a.budget, true, func(ctx context.Context) error {
			var err error
			cents, err = a.price(ctx, r.PathValue("sku"))
			return err
		})
	})
	switch {
	case errors.Is(err, errBulkheadFull):
		overloaded(w, retryAfter)
	case err != nil:
		http.Error(w, "pricing unavailable", http.StatusBadGateway)
	default:
		fmt.Fprintf(w, "%d\n", cents)
	}
}

func main() {
	a := &app{
		price: func(ctx context.Context, sku string) (int, error) {
			return 100 * len(sku), nil
		},
		pool:    newBulkhead(20),
		budget:  newBudget(0.1, 10),
		limiter: newLimiter(50, 10, 500, 250*time.Millisecond),
	}
	srv := &http.Server{
		Addr:              "localhost:8080",
		Handler:           http.TimeoutHandler(a.handler(), 5*time.Second, "timeout"),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
