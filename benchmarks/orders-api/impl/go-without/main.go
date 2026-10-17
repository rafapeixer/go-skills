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
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// The RDS instance allows 80 connections for this service across 8
	// replicas. 8 per replica (64 total) leaves headroom for rolling deploys,
	// where old and new containers briefly run side by side.
	defaultMaxDBConns = 8

	// After SIGTERM, /healthz reports 503 for drainDelay so the load balancer
	// stops routing new traffic before the listener closes. In-flight requests
	// then get up to shutdownTimeout to finish. Keep the sum below the
	// container stop timeout.
	drainDelay      = 5 * time.Second
	shutdownTimeout = 15 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pricingURL, err := parsePricingURL(os.Getenv("PRICING_URL"))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	pool, err := newPool(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	var draining atomic.Bool
	api := &API{
		store:    &Store{pool: pool},
		pricing:  NewPricingClient(pricingURL),
		log:      logger,
		draining: &draining,
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	stop() // a second signal kills the process immediately

	logger.Info("shutdown signal received, draining")
	draining.Store(true)
	time.Sleep(drainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}

func parsePricingURL(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("PRICING_URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("PRICING_URL is not a valid http(s) URL: %q", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

func newPool(ctx context.Context, dbURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	// Respect an explicit pool_max_conns in the URL; otherwise use our budget.
	if !strings.Contains(dbURL, "pool_max_conns") {
		cfg.MaxConns = defaultMaxDBConns
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnLifetimeJitter = 5 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create db pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
