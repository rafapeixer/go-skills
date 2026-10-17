// Command bad compiles, but every marked line is a pool decision that
// exhausts Postgres connections, queues without bound or loses session state.
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func openPool(ctx context.Context) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	// MISTAKE: MaxConns left at its default, max(4, runtime.NumCPU()). On a
	// 64-CPU node every pod opens 64 connections, and 20 pods exceed max_connections.
	// MISTAKE: no MaxConnLifetimeJitter; connections opened together at start
	// all expire together every hour and reconnect in a burst.
	cfg.MaxConnLifetimeJitter = 500 * time.Millisecond // MISTAKE: pgx rounds jitter down to whole seconds, so this is zero.
	// MISTAKE: no PingTimeout; a half-dead connection eats the whole request deadline
	// in the acquire-time ping instead of being dropped after a second.

	// MISTAKE: simple protocol "to make PgBouncer work". It interpolates arguments
	// client side (the path of the v5.9.2 SQL injection fix) and drops binary encoding;
	// PgBouncer 1.21+ supports the default prepared statements, and exec covers older ones.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	return pgxpool.NewWithConfig(ctx, cfg)
}

// MISTAKE: a second pool through database/sql for "compatibility". It has its own
// limits on top of pgxpool, so the service holds twice the connections it was budgeted,
// and code on it cannot use COPY, LISTEN/NOTIFY or pgx batches.
func openLegacy() (*sql.DB, error) {
	return sql.Open("pgx", os.Getenv("DATABASE_URL"))
}

func handler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var n int
		// MISTAKE: context.Background() instead of r.Context() with a deadline. When the
		// pool is full this waits forever, and the queue grows until the pod runs out of memory.
		err := pool.QueryRow(context.Background(), "select count(*) from jobs").Scan(&n)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func runExclusive(ctx context.Context, pool *pgxpool.Pool, job func() error) error {
	// MISTAKE: each pool.Exec may run on a different connection. The lock is taken on
	// one, the unlock runs on another and fails, and the lock leaks until that conn closes.
	// Behind a transaction pooler the server connection changes between statements too.
	if _, err := pool.Exec(ctx, "select pg_advisory_lock(42)"); err != nil {
		return err
	}
	defer pool.Exec(ctx, "select pg_advisory_unlock(42)")
	return job()
}

func listen(ctx context.Context, pool *pgxpool.Pool) error {
	// MISTAKE: LISTEN through the pool registers on a connection that goes straight back
	// to the pool; nobody waits on it, so every notification is lost.
	_, err := pool.Exec(ctx, "listen jobs")
	return err
}

func main() {
	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if _, err := openLegacy(); err != nil {
		log.Fatal(err)
	}
	_ = runExclusive(ctx, pool, func() error { return nil })
	_ = listen(ctx, pool)
	log.Fatal(http.ListenAndServe(":8080", handler(pool)))
}
