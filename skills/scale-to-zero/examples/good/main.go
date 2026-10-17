// Command good starts cheap and stops correctly on a scale-to-zero platform:
// it listens before any optional dependency exists, builds each dependency on
// first use behind sync.OnceValues, and drains on SIGTERM within the grace period.
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
	"syscall"
	"time"
)

// shutdownBudget stays under the platform's signal-to-SIGKILL window with a
// margin: Cloud Run gives 10s, Fly.io's kill_timeout defaults to 5s, and
// Kubernetes' terminationGracePeriodSeconds defaults to 30s.
const shutdownBudget = 8 * time.Second

func main() {
	start := time.Now()
	if err := run(context.Background(), os.Getenv, os.Stdout, start); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdout io.Writer, start time.Time) error {
	// Cloud Run and Kubernetes send SIGTERM; Fly.io sends SIGINT by default.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	port := getenv("PORT")
	if port == "" {
		port = "8080"
	}
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return fmt.Errorf("listen :%s: %w", port, err)
	}

	// Nothing here dials: the store and the report template are built on first use.
	a := newApp(getenv("DATABASE_URL"), openStore)
	defer a.close()
	srv := &http.Server{
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("listening", "addr", ln.Addr().String(), "startup", time.Since(start))
	return serve(ctx, srv, ln, shutdownBudget)
}

// serve runs srv until ctx is done, then gives in-flight requests at most
// budget to finish. See the http-server skill for the full shutdown order.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, budget time.Duration) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return errors.Join(fmt.Errorf("shutdown: %w", err), srv.Close())
	}
	return nil
}
