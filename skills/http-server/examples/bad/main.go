// Command bad compiles and serves requests, but every marked line is a mistake
// that leaves the server open to slow clients and huge bodies, exposes debug
// endpoints, or drops requests on every deploy.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	// MISTAKE: registers /debug/pprof/ on http.DefaultServeMux as a side effect,
	// so the public server below exposes CPU profiles and heap dumps.
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

type database struct{}

func (database) ping(context.Context) error { return nil }

var (
	db    database
	mu    sync.Mutex
	items = map[string]string{}
)

func handleItems(w http.ResponseWriter, r *http.Request) {
	// MISTAKE: parses the path by hand instead of a "GET /items/{id}" pattern and r.PathValue.
	id := strings.TrimPrefix(r.URL.Path, "/items/")
	mu.Lock()
	defer mu.Unlock()
	// MISTAKE: routes on r.Method by hand; DELETE or PUT fall through to the
	// POST branch instead of getting the mux's 405 with an Allow header.
	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(items[id])
		return
	}
	var in struct{ ID, Name string }
	// MISTAKE: decodes an unbounded body; one multi-gigabyte request exhausts memory.
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items[in.ID] = in.Name
	w.WriteHeader(http.StatusCreated)
}

// MISTAKE: changes state on GET; CSRF protection always lets GET through,
// and link prefetchers and crawlers trigger it.
func handleDelete(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	delete(items, r.URL.Query().Get("id"))
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	// MISTAKE: liveness depends on the database; when it blips, every replica
	// fails liveness and the orchestrator restarts the whole fleet at once.
	if err := db.ping(r.Context()); err != nil {
		http.Error(w, "db down", http.StatusServiceUnavailable)
		return
	}
}

func main() {
	// MISTAKE: routes go on the global DefaultServeMux, which any imported
	// package can add to, so the API surface is not what this file shows.
	http.HandleFunc("/items/", handleItems)
	http.HandleFunc("/items/delete", handleDelete)
	http.HandleFunc("/healthz", handleHealthz)

	// MISTAKE: no ReadHeaderTimeout, ReadTimeout, WriteTimeout or IdleTimeout,
	// same as http.ListenAndServe; a client sending one header byte a minute
	// holds a connection and a goroutine forever (Slowloris).
	srv := &http.Server{Addr: ":8080"}

	go func() {
		// MISTAKE: ListenAndServe returns ErrServerClosed as soon as Shutdown
		// starts; log.Fatal then exits and cuts off every in-flight request.
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	// MISTAKE: no readiness flip, so the load balancer keeps sending traffic
	// to a closed listener; and ctx is already cancelled, so Shutdown returns
	// context.Canceled at once instead of waiting for requests to finish.
	srv.Shutdown(ctx)
}
