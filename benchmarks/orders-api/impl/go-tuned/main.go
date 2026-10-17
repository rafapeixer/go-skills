// Command orders-api serves the orders HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// Shutdown budget: drainDelay + shutdownTimeout must stay below the
	// container stop timeout (10s by default for docker stop). Raise both if
	// the orchestrator grants a longer grace period.
	drainDelay      = 3 * time.Second
	shutdownTimeout = 6 * time.Second

	// The t4g.micro has 1 GiB; leave headroom for the runtime and the OS.
	defaultMemoryLimit = 768 << 20
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(context.Background(), logger, os.Getenv); err != nil {
		logger.Error("exit", "err", err)
		os.Exit(1)
	}
}

type config struct {
	port        string
	databaseURL string
	pricingURL  *url.URL
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		port:        getenv("PORT"),
		databaseURL: getenv("DATABASE_URL"),
	}
	if cfg.port == "" {
		cfg.port = "8080"
	}
	if cfg.databaseURL == "" {
		return config{}, errors.New("DATABASE_URL is required")
	}
	raw := getenv("PRICING_URL")
	if raw == "" {
		return config{}, errors.New("PRICING_URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return config{}, fmt.Errorf("parse PRICING_URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return config{}, errors.New("PRICING_URL must be an http(s) base URL without query or fragment")
	}
	cfg.pricingURL = u
	return cfg, nil
}

func run(ctx context.Context, logger *slog.Logger, getenv func(string) string) error {
	if getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(defaultMemoryLimit)
	}

	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	st, err := newStore(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer st.close()

	var shuttingDown atomic.Bool
	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           newHandler(logger, st, newPricingClient(cfg.pricingURL), &shuttingDown),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		// Longer than the load balancer's idle timeout (60s on ALB), so the
		// balancer, not the server, closes idle keep-alive connections.
		IdleTimeout: 75 * time.Second,
		ErrorLog:    slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go serveDebug(":6060")
	go func() { serveErr <- srv.ListenAndServe() }()
	logger.Info("listening", "addr", srv.Addr)

	select {
	case err := <-serveErr:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shuttingDown.Store(true)
	time.Sleep(drainDelay)

	// ctx is already cancelled; WithoutCancel keeps its values, not its Done.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return errors.Join(fmt.Errorf("shutdown: %w", err), srv.Close())
	}
	logger.Info("stopped")
	return nil
}
