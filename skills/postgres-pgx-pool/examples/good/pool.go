// Package main shows a pgxpool sized from the database's connection budget,
// with jittered lifetimes, a ping timeout and a bounded acquire that turns
// pool exhaustion into 503 instead of an unbounded queue.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPoolExhausted means no connection became free within the acquire wait.
var ErrPoolExhausted = errors.New("database pool exhausted")

const (
	maxConnLifetime = time.Hour
	pingTimeout     = time.Second
	acquireWait     = 100 * time.Millisecond
)

// newPoolConfig parses dsn and sizes the pool so that replicas pods together
// never open more than budget connections. replicas is the maximum pod count
// including autoscaling and rolling-update surge.
func newPoolConfig(dsn string, budget, replicas int) (*pgxpool.Config, error) {
	if replicas < 1 || budget < replicas {
		return nil, fmt.Errorf("size pool: budget %d is too small for %d replicas", budget, replicas)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}
	if cfg.ConnConfig.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
		return nil, errors.New("parse database dsn: simple_protocol interpolates arguments client side; use exec behind poolers")
	}
	cfg.MaxConns = int32(budget / replicas)
	cfg.MaxConnLifetime = maxConnLifetime
	cfg.MaxConnLifetimeJitter = maxConnLifetime / 10
	cfg.PingTimeout = pingTimeout
	return cfg, nil
}

// withConn waits at most acquireWait for a connection. A full pool is
// backpressure: the caller gets ErrPoolExhausted and sheds the request.
func withConn(ctx context.Context, pool *pgxpool.Pool, f func(*pgxpool.Conn) error) error {
	actx, cancel := context.WithTimeout(ctx, acquireWait)
	conn, err := pool.Acquire(actx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return ErrPoolExhausted
		}
		return fmt.Errorf("acquire database connection: %w", err)
	}
	defer conn.Release()
	return f(conn)
}

// statusFor maps pool exhaustion to 503 so load balancers and clients back off.
func statusFor(err error) int {
	if errors.Is(err, ErrPoolExhausted) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}
