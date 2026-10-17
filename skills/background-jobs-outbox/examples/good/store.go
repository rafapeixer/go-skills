package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

type orders struct {
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
}

// PlaceOrder writes the order, its receipt job and its outbox event in one
// transaction: all three commit together or none of them exists. Nothing
// here talks to the network; workers and the relay do that after commit.
func (o *orders) PlaceOrder(ctx context.Context, customerID, totalCents int64) (int64, error) {
	var id int64
	err := pgx.BeginTxFunc(ctx, o.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, insertOrderSQL, customerID, totalCents).Scan(&id); err != nil {
			return fmt.Errorf("insert order: %w", err)
		}
		if _, err := o.river.InsertTx(ctx, tx, SendReceiptArgs{OrderID: id}, nil); err != nil {
			return fmt.Errorf("enqueue receipt: %w", err)
		}
		payload := fmt.Sprintf(`{"order_id":%d}`, id)
		if _, err := tx.Exec(ctx, insertOutboxSQL, "order.placed", payload); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
		// Delivered only if the transaction commits. It is a hint, not the
		// event: the row above is the event, and the relay also polls.
		if _, err := tx.Exec(ctx, "SELECT pg_notify($1, '')", outboxChannel); err != nil {
			return fmt.Errorf("notify relay: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("place order for customer %d: %w", customerID, err)
	}
	return id, nil
}

// pgStore records sent receipts; a unique key on order_id makes the mark
// itself idempotent.
type pgStore struct{ pool *pgxpool.Pool }

func (s pgStore) ReceiptSent(ctx context.Context, orderID int64) (bool, error) {
	var sent bool
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM receipts WHERE order_id = $1)", orderID).Scan(&sent)
	return sent, err
}

func (s pgStore) MarkReceiptSent(ctx context.Context, orderID int64) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO receipts (order_id) VALUES ($1) ON CONFLICT (order_id) DO NOTHING", orderID)
	return err
}
