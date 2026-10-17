package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PlaceOrder inserts the order and its "order.placed" event in one
// transaction. The confirmation email is sent by the outbox relay after
// commit, so a retry or a rollback never sends it twice or for nothing.
func PlaceOrder(ctx context.Context, pool *pgxpool.Pool, customerID, totalCents int64) (int64, error) {
	var id int64
	err := InTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		q := New(tx)
		var err error
		id, err = q.InsertOrder(ctx, InsertOrderParams{CustomerID: customerID, TotalCents: totalCents})
		if err != nil {
			return fmt.Errorf("insert order: %w", err)
		}
		payload, err := json.Marshal(map[string]int64{"order_id": id, "customer_id": customerID})
		if err != nil {
			return fmt.Errorf("encode order event: %w", err)
		}
		return q.InsertOutbox(ctx, InsertOutboxParams{Topic: "order.placed", Payload: payload})
	})
	if err != nil {
		return 0, fmt.Errorf("place order for customer %d: %w", customerID, err)
	}
	return id, nil
}

// OrdersFor loads the orders of every customer on a page in one round trip,
// instead of one query per customer.
func OrdersFor(ctx context.Context, q *Queries, customerIDs []int64) (map[int64][]Order, error) {
	orders, err := q.OrdersByCustomers(ctx, customerIDs)
	if err != nil {
		return nil, fmt.Errorf("load orders for %d customers: %w", len(customerIDs), err)
	}
	byCustomer := make(map[int64][]Order, len(customerIDs))
	for _, o := range orders {
		byCustomer[o.CustomerID] = append(byCustomer[o.CustomerID], o)
	}
	return byCustomer, nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Size and configure the pool as the postgres-pgx-pool skill describes.
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		slog.Error("create database pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	id, err := PlaceOrder(ctx, pool, 7, 2599)
	if err != nil {
		slog.Error("place order", "err", err)
		return
	}
	page, next, err := ListOrders(ctx, pool, 7, "newest", "", 20)
	if err != nil {
		slog.Error("list orders", "err", err)
		return
	}
	byCustomer, err := OrdersFor(ctx, New(pool), []int64{7, 8, 9})
	if err != nil {
		slog.Error("load orders", "err", err)
		return
	}
	slog.Info("orders", "placed", id, "page", len(page), "next", next, "customers", len(byCustomer))
}
