// Command good is an HTTP server built on net/http alone: explicit timeouts,
// its own ServeMux, bounded request bodies, and a shutdown that fails
// readiness first and then drains in-flight requests.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	drainDelay      = 5 * time.Second  // time for the load balancer to see /readyz fail
	shutdownTimeout = 20 * time.Second // drainDelay + shutdownTimeout < the orchestrator's grace period
)

func main() {
	if err := run(context.Background(), os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	addr := getenv("ADDR")
	if addr == "" {
		addr = "localhost:8080"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	var shuttingDown atomic.Bool
	srv := newServer(newHandler(logger, newStore(), &shuttingDown))
	logger.Info("listening", "addr", ln.Addr().String())
	return serve(ctx, srv, ln, &shuttingDown, drainDelay)
}

func newServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// serve runs srv until ctx is done, then shuts down in order: readiness
// fails, traffic drains away, and Shutdown waits for in-flight requests.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, shuttingDown *atomic.Bool, drain time.Duration) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shuttingDown.Store(true)
	time.Sleep(drain)

	// ctx is already cancelled; WithoutCancel keeps its values, not its Done.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return errors.Join(fmt.Errorf("shutdown: %w", err), srv.Close())
	}
	return nil
}
