package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const dsn = "postgres://app@db.internal:5432/app?sslmode=verify-full"

// ParseConfig does not connect, so these tests need no database.
func TestNewPoolConfigSizing(t *testing.T) {
	cfg, err := newPoolConfig(dsn+"&pool_max_conns=100", 90, 6)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 15 {
		t.Errorf("MaxConns = %d, want 15 (budget / replicas wins over the DSN)", cfg.MaxConns)
	}
	if cfg.MaxConnLifetimeJitter < time.Second {
		t.Errorf("MaxConnLifetimeJitter = %v; below 1s pgx rounds the jitter to zero", cfg.MaxConnLifetimeJitter)
	}
	if cfg.PingTimeout <= 0 {
		t.Errorf("PingTimeout = %v, want a bound", cfg.PingTimeout)
	}
	if got := cfg.ConnConfig.DefaultQueryExecMode; got != pgx.QueryExecModeCacheStatement {
		t.Errorf("DefaultQueryExecMode = %v, want the cache_statement default", got)
	}
}

func TestNewPoolConfigRejects(t *testing.T) {
	tests := map[string]struct {
		dsn              string
		budget, replicas int
	}{
		"budget below replicas": {dsn, 4, 6},
		"simple protocol":       {dsn + "&default_query_exec_mode=simple_protocol", 90, 6},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := newPoolConfig(tt.dsn, tt.budget, tt.replicas); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// Pin the production DSN shape: the pgx 5.11 parser follows libpq, so a
// DSN that parsed differently before fails here, not in the deploy.
func TestDSNBehindPgBouncer(t *testing.T) {
	cfg, err := newPoolConfig("postgres://app@pgbouncer:6432/app?default_query_exec_mode=exec", 90, 6)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Host != "pgbouncer" || cfg.ConnConfig.Port != 6432 {
		t.Errorf("host = %s:%d, want pgbouncer:6432", cfg.ConnConfig.Host, cfg.ConnConfig.Port)
	}
	if got := cfg.ConnConfig.DefaultQueryExecMode; got != pgx.QueryExecModeExec {
		t.Errorf("DefaultQueryExecMode = %v, want exec", got)
	}
}

func TestWithConnShedsLoadWhenNoConnection(t *testing.T) {
	cfg, err := newPoolConfig(dsn, 90, 6)
	if err != nil {
		t.Fatal(err)
	}
	// No DNS, and a dial that never completes stands in for a saturated database.
	cfg.ConnConfig.LookupFunc = func(_ context.Context, host string) ([]string, error) {
		return []string{host}, nil
	}
	cfg.ConnConfig.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	err = withConn(t.Context(), pool, func(*pgxpool.Conn) error { return nil })
	if !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("withConn error = %v, want ErrPoolExhausted", err)
	}
	if got := statusFor(err); got != http.StatusServiceUnavailable {
		t.Errorf("statusFor = %d, want 503", got)
	}
}
