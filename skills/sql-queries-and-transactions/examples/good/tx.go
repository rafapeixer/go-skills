// Package main shows Postgres queries and transactions with pgx v5: sqlc
// queries, one transaction helper that reruns the whole transaction on a
// serialization failure or deadlock, keyset pagination and an allow-listed
// sort order.
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	serializationFailure = "40001"
	deadlockDetected     = "40P01"

	maxAttempts = 5
	baseBackoff = 10 * time.Millisecond
	maxBackoff  = 500 * time.Millisecond
)

// beginner is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// InTx runs fn in a transaction and commits when fn returns nil. On 40001 or
// 40P01 it rolls back and runs fn again from the start, so fn must do nothing
// but database work: no HTTP calls, no publishing, no state outside the tx.
func InTx(ctx context.Context, db beginner, opts pgx.TxOptions, fn func(pgx.Tx) error) error {
	return retryTx(ctx, func() error {
		return pgx.BeginTxFunc(ctx, db, opts, fn)
	})
}

// retryTx calls attempt until it succeeds, fails with an error that is not
// retryable, runs out of attempts or ctx ends. It waits a capped backoff with
// full jitter between attempts so conflicting transactions do not collide again.
func retryTx(ctx context.Context, attempt func() error) error {
	var err error
	for n := range maxAttempts {
		if n > 0 {
			backoff := min(baseBackoff<<(n-1), maxBackoff)
			select {
			case <-ctx.Done():
				return fmt.Errorf("retry transaction after %w: %w", err, context.Cause(ctx))
			case <-time.After(rand.N(backoff)):
			}
		}
		if err = attempt(); err == nil || !retryable(err) {
			return err
		}
	}
	return fmt.Errorf("transaction gave up after %d attempts: %w", maxAttempts, err)
}

// retryable reports whether Postgres aborted the transaction for a reason that
// a fresh attempt can fix. Everything else, including 23505 unique_violation
// and 57014 query_canceled from statement_timeout, goes back to the caller.
func retryable(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (pgErr.Code == serializationFailure || pgErr.Code == deadlockDetected)
}
