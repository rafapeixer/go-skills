package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
)

// auditor owns one goroutine. Handlers hand events to it over a channel
// instead of starting goroutines of their own.
type auditor struct {
	events chan string
	wg     sync.WaitGroup
}

func startAuditor(logger *slog.Logger, buffer int) *auditor {
	a := &auditor{events: make(chan string, buffer)}
	a.wg.Go(func() {
		for e := range a.events {
			logger.Info("audit", "event", e)
		}
	})
	return a
}

// Record never blocks a request: when the buffer is full it reports false,
// so the caller decides what to do instead of piling up goroutines.
func (a *auditor) Record(event string) bool {
	select {
	case a.events <- event:
		return true
	default:
		return false
	}
}

// Stop drains the queue and waits for the goroutine to exit. Call it after
// the HTTP server has shut down, so no handler can Record any more.
func (a *auditor) Stop() {
	close(a.events)
	a.wg.Wait()
}

func pricesHandler(c *cache, a *auditor, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ps, err := prices(r.Context(), r.URL.Query()["sku"], c.lookup)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "unknown sku", http.StatusNotFound)
			return
		}
		if err != nil {
			logger.Error("prices", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !a.Record("priced " + r.URL.RawQuery) {
			logger.Warn("audit queue full, event dropped")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ps)
	}
}
