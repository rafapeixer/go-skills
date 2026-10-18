// Command orders-api serves the orders HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// The RDS instance allows 80 connections for this service. Deploys can run
	// the old and new task sets side by side (ECS maximumPercent defaults to
	// 200%), so up to 16 replicas may hold connections at once: 80 / 16 = 5.
	dbConnBudget = 80
	maxReplicas  = 16
	dbMaxConns   = dbConnBudget / maxReplicas

	dbConnLifetime = time.Hour

	// A t4g.micro has 1 GiB shared with the OS and container runtime; keep the
	// Go heap's soft limit well below it unless GOMEMLIMIT is set explicitly.
	defaultMemLimit = 700 << 20

	// The drain delay lets the load balancer stop routing to this replica
	// before the listener closes; together with shutdownTimeout it stays under
	// the 30 s stop timeout container orchestrators use by default.
	drainDelay      = 5 * time.Second
	shutdownTimeout = 20 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(ctx, logger, os.Getenv); err != nil {
		logger.Error("orders-api stopped", "err", err)
		os.Exit(1)
	}
}

type config struct {
	port        string
	databaseURL string
	pricingURL  *url.URL
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{port: getenv("PORT"), databaseURL: getenv("DATABASE_URL")}
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
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return config{}, fmt.Errorf("PRICING_URL must be an absolute http(s) URL, got %q", raw)
	}
	cfg.pricingURL = u
	return cfg, nil
}

func newPoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if cfg.ConnConfig.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
		return nil, errors.New("parse DATABASE_URL: simple_protocol is not allowed; use exec behind poolers")
	}
	// Sizing is owned here so a pool_max_conns in the DSN cannot exceed the budget.
	cfg.MaxConns = dbMaxConns
	cfg.MaxConnLifetime = dbConnLifetime
	cfg.MaxConnLifetimeJitter = dbConnLifetime / 10
	cfg.PingTimeout = time.Second
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	}

	// Server-side backstops for the request deadlines, in case this process
	// dies or is partitioned while a statement or transaction is open.
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	for k, v := range map[string]string{
		"application_name":                    "orders-api",
		"statement_timeout":                   "5s",
		"idle_in_transaction_session_timeout": "10s",
	} {
		if _, ok := cfg.ConnConfig.RuntimeParams[k]; !ok {
			cfg.ConnConfig.RuntimeParams[k] = v
		}
	}
	return cfg, nil
}

func run(ctx context.Context, logger *slog.Logger, getenv func(string) string) error {
	if getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(defaultMemLimit)
	}

	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	poolCfg, err := newPoolConfig(cfg.databaseURL)
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()

	// Not fatal: the database may be failing over. /readyz reports it until it recovers.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := pool.Ping(pingCtx); err != nil {
		logger.Warn("database not reachable at startup", "err", err)
	}
	cancel()

	a := &app{
		store:   &store{pool: pool},
		pricing: newPricingClient(cfg.pricingURL),
		logger:  logger,
	}
	var shuttingDown atomic.Bool
	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           newHandler(a, pool, &shuttingDown),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	logger.Info("listening", "addr", ln.Addr().String(), "db_max_conns", dbMaxConns)

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutdown started")
	shuttingDown.Store(true)
	time.Sleep(drainDelay)

	// ctx is already cancelled; WithoutCancel keeps its values, not its Done.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return errors.Join(fmt.Errorf("shutdown: %w", err), srv.Close())
	}
	logger.Info("shutdown complete")
	return nil
}
