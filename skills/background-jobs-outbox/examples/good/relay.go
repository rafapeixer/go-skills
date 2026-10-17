package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runRelay drains the outbox until ctx ends. A wake-up from LISTEN/NOTIFY
// makes it drain early; the ticker makes it drain anyway, because a
// notification is lost whenever no listener is connected (deploys, network
// blips, PgBouncer in transaction mode).
func runRelay(ctx context.Context, wake <-chan struct{}, poll time.Duration, drain func(context.Context) error) {
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if err := drain(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "drain outbox", "err", err)
		}
		select {
		case <-ctx.Done():
			return // Unfinished rows are leased and come back after leaseFor.
		case <-wake:
		case <-t.C:
		}
	}
}

// drainOutbox claims one batch and publishes it, recording each outcome.
func drainOutbox(pool *pgxpool.Pool, publish func(context.Context, event) error) func(context.Context) error {
	return func(ctx context.Context) error {
		rows, err := pool.Query(ctx, claimSQL, claimBatch, leaseFor.Seconds())
		if err != nil {
			return fmt.Errorf("claim outbox: %w", err)
		}
		var batch []event
		for rows.Next() {
			var e event
			if err := rows.Scan(&e.ID, &e.Topic, &e.Payload, &e.Attempts); err != nil {
				rows.Close()
				return fmt.Errorf("scan outbox: %w", err)
			}
			batch = append(batch, e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("claim outbox: %w", err)
		}
		for _, e := range batch {
			if err := publish(ctx, e); err != nil {
				if dead, delay := afterFailure(e.Attempts); dead {
					_, err = pool.Exec(ctx, markDeadSQL, e.ID, err.Error())
				} else {
					_, err = pool.Exec(ctx, markRetrySQL, e.ID, delay.Seconds(), err.Error())
				}
				if err != nil {
					return fmt.Errorf("record failure of event %d: %w", e.ID, err)
				}
				continue
			}
			if _, err := pool.Exec(ctx, markSentSQL, e.ID); err != nil {
				return fmt.Errorf("mark event %d sent: %w", e.ID, err)
			}
		}
		return nil
	}
}

// listen turns notifications into non-blocking wake-ups on a dedicated
// connection. It never carries data: the payload is empty on purpose.
func listen(ctx context.Context, pool *pgxpool.Pool, wake chan<- struct{}) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listener: %w", err)
	}
	// A connection that has run LISTEN keeps that session state, so it must
	// never go back to the pool for a request to inherit.
	defer func() { _ = conn.Hijack().Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+outboxChannel); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return fmt.Errorf("wait for notification: %w", err)
		}
		select {
		case wake <- struct{}{}:
		default: // A wake-up is already pending; one drain covers both.
		}
	}
}

// publishLog stands in for a broker client.
func publishLog(ctx context.Context, e event) error {
	slog.InfoContext(ctx, "publish", "topic", e.Topic, "id", e.ID)
	return nil
}
