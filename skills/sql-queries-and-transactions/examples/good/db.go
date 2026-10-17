package main

// This file is what `sqlc generate` writes from query.sql and sqlc.yaml
// (sql_package: "pgx/v5"). It is written by hand here so the example builds
// without sqlc; in a service, commit the generated file and never edit it.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func New(db DBTX) *Queries { return &Queries{db: db} }

type Queries struct{ db DBTX }

func (q *Queries) WithTx(tx pgx.Tx) *Queries { return &Queries{db: tx} }

type Order struct {
	ID         int64     `db:"id"`
	CustomerID int64     `db:"customer_id"`
	TotalCents int64     `db:"total_cents"`
	CreatedAt  time.Time `db:"created_at"`
}

const ordersByCustomers = `-- name: OrdersByCustomers :many
SELECT id, customer_id, total_cents, created_at
FROM orders
WHERE customer_id = ANY($1::bigint[])
ORDER BY customer_id, created_at DESC, id DESC
`

func (q *Queries) OrdersByCustomers(ctx context.Context, customerIds []int64) ([]Order, error) {
	rows, err := q.db.Query(ctx, ordersByCustomers, customerIds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Order
	for rows.Next() {
		var i Order
		if err := rows.Scan(&i.ID, &i.CustomerID, &i.TotalCents, &i.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

const insertOrder = `-- name: InsertOrder :one
INSERT INTO orders (customer_id, total_cents)
VALUES ($1, $2)
RETURNING id
`

type InsertOrderParams struct {
	CustomerID int64 `db:"customer_id"`
	TotalCents int64 `db:"total_cents"`
}

func (q *Queries) InsertOrder(ctx context.Context, arg InsertOrderParams) (int64, error) {
	var id int64
	err := q.db.QueryRow(ctx, insertOrder, arg.CustomerID, arg.TotalCents).Scan(&id)
	return id, err
}

const insertOutbox = `-- name: InsertOutbox :exec
INSERT INTO outbox (topic, payload)
VALUES ($1, $2)
`

type InsertOutboxParams struct {
	Topic   string `db:"topic"`
	Payload []byte `db:"payload"`
}

func (q *Queries) InsertOutbox(ctx context.Context, arg InsertOutboxParams) error {
	_, err := q.db.Exec(ctx, insertOutbox, arg.Topic, arg.Payload)
	return err
}
