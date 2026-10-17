// Package main runs background work on Postgres: River jobs enqueued with
// InsertTx in the business transaction, and an outbox relay that claims rows
// with FOR UPDATE SKIP LOCKED and uses LISTEN/NOTIFY only as a wake-up.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const (
	queueReceipts = "receipts"
	poolMaxConns  = 20
	// The platform kills the pod this long after SIGTERM (Kubernetes'
	// default terminationGracePeriodSeconds is 30).
	shutdownGrace = 30 * time.Second
	relayPoll     = 5 * time.Second
)

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = poolMaxConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create pool: %w", err)
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	// "worker migrate" runs once per deploy, before the rollout, next to the
	// schema migrations. Workers never migrate; they refuse an old schema.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		_, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
		return err
	}
	res, err := migrator.Validate(ctx, nil)
	if err != nil {
		return fmt.Errorf("validate river schema: %w", err)
	}
	if !res.OK {
		return fmt.Errorf("river schema not migrated: %v", res.Messages)
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, &SendReceiptWorker{store: pgStore{pool}, mailer: logMailer{}})
	client, err := river.NewClient(riverpgxv5.New(pool), riverConfig(workers, poolMaxConns))
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}

	// Cancelling ctx starts a soft stop: River stops fetching, lets running
	// jobs finish for SoftStopTimeout, then cancels their contexts.
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start river: %w", err)
	}
	var wg sync.WaitGroup
	wake := make(chan struct{}, 1)
	// The listener ends with ctx; while it is down the poll still drains.
	wg.Go(func() { _ = listen(ctx, pool, wake) })
	runRelay(ctx, wake, relayPoll, drainOutbox(pool, publishLog))
	wg.Wait()
	<-client.Stopped()
	return nil
}

// riverConfig ties worker concurrency to the pool: every running job may
// hold a connection, so the workers get half and requests keep the rest.
func riverConfig(workers *river.Workers, maxConns int32) *river.Config {
	return &river.Config{
		Queues: map[string]river.QueueConfig{
			queueReceipts: {MaxWorkers: int(maxConns) / 2},
		},
		Workers:         workers,
		SoftStopTimeout: shutdownGrace - 10*time.Second,
	}
}
