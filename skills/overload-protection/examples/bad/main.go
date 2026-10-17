// Command bad compiles, but above capacity it queues instead of shedding,
// multiplies retries across layers and retries writes that are not safe to
// repeat. Every marked line is a mistake that turns a slow dependency into
// an outage that outlives the slowdown.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

// MISTAKE: a semaphore with a blocking acquire is a queue, not a limit.
// When it is full, every new request parks a goroutine here until its
// client has long given up, and the backlog keeps the server overloaded.
var inFlight = make(chan struct{}, 100)

func limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight <- struct{}{} // MISTAKE: waits forever, no 503 and no Retry-After
		defer func() { <-inFlight }()
		next.ServeHTTP(w, r)
	})
}

// callWithRetry retries any error, five times, one second apart.
func callWithRetry(ctx context.Context, op func(context.Context) error) error {
	var err error
	for range 5 { // MISTAKE: 5 attempts per layer; nested twice, inventory gets 25 calls
		if err = op(ctx); err == nil {
			return nil
		}
		// MISTAKE: a fixed sleep, no jitter: every client that failed together
		// retries together, in synchronized waves. It also ignores ctx, so it
		// keeps sleeping and retrying after the request deadline has passed.
		time.Sleep(time.Second)
	}
	return err
}

func chargeCard(ctx context.Context, orderID string) error {
	return errors.New("payments: 503")
}

func reserveStock(ctx context.Context, orderID string) error {
	// MISTAKE: the client library retries too, so this layer multiplies
	// whatever the handler above it already does.
	return callWithRetry(ctx, func(ctx context.Context) error {
		return errors.New("inventory: timeout")
	})
}

func createOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.FormValue("id")
	// MISTAKE: no ctx.Err() check; work starts even for a client that hung up.
	err := callWithRetry(ctx, func(ctx context.Context) error {
		if err := reserveStock(ctx, id); err != nil {
			return err
		}
		// MISTAKE: retrying a POST that charges a card, with no idempotency
		// key: a timeout after the charge succeeded bills the customer twice.
		return chargeCard(ctx, id)
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, "ok")
}

func search(w http.ResponseWriter, r *http.Request) {
	// MISTAKE: no bulkhead; a slow search backend holds every in-flight slot,
	// so checkout, which never touches search, starts failing too.
	if err := callWithRetry(r.Context(), func(ctx context.Context) error {
		time.Sleep(10 * time.Second) // stands in for a slow search backend
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", createOrder)
	mux.HandleFunc("GET /search", search)
	srv := &http.Server{Addr: ":8080", Handler: limit(mux), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
