// Command bad compiles, but every marked line is a concurrency mistake that
// races, leaks goroutines or loses work under load and at shutdown.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")

type Price struct {
	SKU   string
	Cents int
}

// MISTAKE: a plain map shared by every request goroutine with no mutex;
// concurrent writes crash the process with "concurrent map writes".
var cache = map[string]Price{}

func lookup(ctx context.Context, sku string) (Price, error) {
	time.Sleep(10 * time.Millisecond) // MISTAKE: ignores ctx, so cancelling it stops nothing
	if sku == "" {
		return Price{}, ErrNotFound
	}
	p := Price{SKU: sku, Cents: 100 * len(sku)}
	cache[sku] = p
	return p, nil
}

func prices(ctx context.Context, skus []string) ([]Price, error) {
	var wg sync.WaitGroup
	var firstErr error
	var out []Price
	for _, sku := range skus {
		// MISTAKE: one goroutine per input item with no limit; 10,000 SKUs
		// open 10,000 queries against a pool of a few dozen connections.
		go func() {
			// MISTAKE: Add inside the new goroutine races with Wait, which can
			// return before this goroutine is counted. go vet reports it.
			wg.Add(1)
			defer wg.Done()
			p, err := lookup(ctx, sku)
			if err != nil {
				// MISTAKE: unsynchronized write from many goroutines is a data
				// race, and nothing cancels the sibling lookups still running.
				firstErr = err
				return
			}
			out = append(out, p) // MISTAKE: concurrent append loses results or corrupts the slice.
		}()
	}
	wg.Wait()
	return out, firstErr
}

// firstFailure reports the first lookup error, if any.
func firstFailure(ctx context.Context, skus []string) error {
	errs := make(chan error) // MISTAKE: unbuffered, and the reader may stop early
	for _, sku := range skus {
		go func() {
			_, err := lookup(ctx, sku)
			errs <- err
		}()
	}
	for range skus {
		if err := <-errs; err != nil {
			// MISTAKE: early return leaves the remaining senders blocked on
			// errs forever; every call leaks goroutines until the pod OOMs.
			return err
		}
	}
	return nil
}

func audit(ctx context.Context, event string) {
	if ctx.Err() == nil { // stands in for a write that honours ctx
		log.Print("audit: ", event)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	ps, err := prices(r.Context(), r.URL.Query()["sku"])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// MISTAKE: fire-and-forget. Nobody waits for it at shutdown, there is no
	// bound on how many pile up, and r.Context() is cancelled when the handler
	// returns, so the audit is usually dropped.
	go audit(r.Context(), fmt.Sprintf("priced %d skus", len(ps)))
	fmt.Fprintf(w, "%v\n", ps)
}

func main() {
	http.HandleFunc("/prices", handler)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
