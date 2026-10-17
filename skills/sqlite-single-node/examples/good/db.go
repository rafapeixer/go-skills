package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"runtime"

	_ "modernc.org/sqlite" // cgo-free driver, registered as "sqlite"
)

// pragmas run, in order, on every connection the driver opens. Only
// journal_mode=WAL is stored in the file; the other three are per connection.
// busy_timeout goes first so that switching to WAL waits for a lock.
var pragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(ON)",
}

// DB is one SQLite file opened twice: one writer connection and a read pool.
type DB struct {
	W *sql.DB // exactly one connection; every transaction is BEGIN IMMEDIATE
	R *sql.DB // many connections; query_only, so a write sent here fails loudly
}

func dsn(path string, extra url.Values) string {
	q := url.Values{"_pragma": pragmas}
	for k, v := range extra {
		q[k] = v
	}
	return path + "?" + q.Encode()
}

// Open opens the writer first, so the file exists and is in WAL mode before
// any reader connects.
func Open(ctx context.Context, path string) (*DB, error) {
	w, err := sql.Open("sqlite", dsn(path, url.Values{"_txlock": {"immediate"}}))
	if err != nil {
		return nil, fmt.Errorf("open writer %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("ping writer %s: %w", path, err)
	}

	r, err := sql.Open("sqlite", dsn(path, url.Values{"_query_only": {"1"}}))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("open readers %s: %w", path, err)
	}
	n := max(4, runtime.GOMAXPROCS(0))
	r.SetMaxOpenConns(n)
	r.SetMaxIdleConns(n)
	return &DB{W: w, R: r}, nil
}

// Write runs fn in a write transaction. The writer DSN sets _txlock=immediate,
// so BeginTx sends BEGIN IMMEDIATE: the write lock is taken, and waited for
// under busy_timeout, before fn reads anything.
func (db *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write: %w", err)
	}
	defer tx.Rollback() // no-op after Commit
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit write: %w", err)
	}
	return nil
}

// Close closes the readers first, so the writer is the last connection and
// checkpoints the WAL back into the database file.
func (db *DB) Close() error {
	return errors.Join(db.R.Close(), db.W.Close())
}
