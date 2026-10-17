// Command good serves a public API and, on a separate loopback listener, the
// pprof and efficiency endpoints. A flight recorder runs from boot and writes
// a trace snapshot when a request misses its latency objective.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// slo is the latency objective; a slower request triggers a trace snapshot.
const slo = 300 * time.Millisecond

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rec, err := startRecorder(os.TempDir(), slo, time.Minute)
	if err != nil {
		return err
	}
	defer rec.stop()

	var stats efficiency
	public := newServer(":8080", stats.count(rec.watch(newPublicMux())))
	// Loopback only: reach it with kubectl port-forward, never through the
	// load balancer. Nothing here is mounted on the public mux.
	internal := newServer("127.0.0.1:6060", newDebugMux(&stats))

	errc := make(chan error, 2)
	for _, srv := range []*http.Server{public, internal} {
		go func() { errc <- srv.ListenAndServe() }()
	}
	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return errors.Join(err, public.Shutdown(shutdownCtx), internal.Shutdown(shutdownCtx))
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second, // pprof extends it for ?seconds=N
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
	}
}

// newPublicMux holds only application routes. The server never falls back to
// http.DefaultServeMux, where importing net/http/pprof registers its handlers.
func newPublicMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /report", handleReport)
	return mux
}
