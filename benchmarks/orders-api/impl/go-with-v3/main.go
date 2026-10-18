// Command orders-api serves orders stored in Postgres and quotes them
// against the pricing service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// The service may hold 80 Postgres connections in total. It runs 8
	// replicas, and a rolling deploy can briefly add 2 more, so each replica
	// gets 80 / 10 = 8 connections. Raise maxReplicas before scaling out.
	dbConnBudget = 80
	maxReplicas  = 10

	maxConnLifetime = 30 * time.Minute
	pingTimeout     = time.Second
	connectTimeout  = 5 * time.Second

	// Server-side backstops for the request deadline (requestTimeout), in
	// case a pod dies or is partitioned while a query is running.
	statementTimeoutMS = "10000"
	idleInTxTimeoutMS  = "10000"

	// Drain plus shutdown stays under a 30s stop grace period.
	drainDelay      = 5 * time.Second
	shutdownTimeout = 15 * time.Second

	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second

	defaultPort = "8080"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := run(ctx, os.Getenv, logger); err != nil {
		logger.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, logger *slog.Logger) error {
	port := getenv("PORT")
	if port == "" {
		port = defaultPort
	}
	dsn := getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	pricingURL := getenv("PRICING_URL")
	if pricingURL == "" {
		return errors.New("PRICING_URL is required")
	}

	poolCfg, err := newPoolConfig(dsn)
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer pool.Close()

	pricing, err := newPricingClient(pricingURL)
	if err != nil {
		return err
	}

	var shuttingDown atomic.Bool
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           newHandler(logger, &store{pool: pool}, pricing, &shuttingDown),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("listening", "addr", srv.Addr, "db_max_conns", poolCfg.MaxConns)

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	// Fail /readyz so the load balancer stops routing here, give it time to
	// notice, then drain in-flight requests.
	logger.Info("shutting down")
	shuttingDown.Store(true)
	time.Sleep(drainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return errors.Join(fmt.Errorf("shutdown: %w", err), srv.Close())
	}
	logger.Info("shutdown complete")
	return nil
}

func newPoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if cfg.ConnConfig.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
		return nil, errors.New("parse DATABASE_URL: simple_protocol is not allowed; use exec behind poolers")
	}
	// Sizing is owned here, so a pool_max_conns in the DSN cannot exceed the budget.
	cfg.MaxConns = int32(dbConnBudget / maxReplicas)
	cfg.MinConns = 0
	cfg.MaxConnLifetime = maxConnLifetime
	cfg.MaxConnLifetimeJitter = maxConnLifetime / 10
	cfg.PingTimeout = pingTimeout
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = connectTimeout
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	rp := cfg.ConnConfig.RuntimeParams
	if _, ok := rp["statement_timeout"]; !ok {
		rp["statement_timeout"] = statementTimeoutMS
	}
	if _, ok := rp["idle_in_transaction_session_timeout"]; !ok {
		rp["idle_in_transaction_session_timeout"] = idleInTxTimeoutMS
	}
	return cfg, nil
}
