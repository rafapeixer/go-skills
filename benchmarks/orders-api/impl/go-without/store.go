package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errNotFound = errors.New("not found")

type Item struct {
	SKU string `json:"sku"`
	Qty int32  `json:"qty"`
}

type Order struct {
	ID         int64     `json:"id"`
	CustomerID int64     `json:"customer_id"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	Items      []Item    `json:"items"`
}

// Cursor is a keyset position in a customer's order list, ordered by
// (created_at DESC, id DESC) to match the orders_customer_created index.
type Cursor struct {
	CreatedAt time.Time
	ID        int64
}

func (c Cursor) Encode() string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(c.CreatedAt.UnixMicro()))
	binary.BigEndian.PutUint64(b[8:], uint64(c.ID))
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func DecodeCursor(s string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != 16 {
		return Cursor{}, errors.New("invalid cursor")
	}
	return Cursor{
		CreatedAt: time.UnixMicro(int64(binary.BigEndian.Uint64(b[:8]))),
		ID:        int64(binary.BigEndian.Uint64(b[8:])),
	}, nil
}

type Store struct {
	pool *pgxpool.Pool
}

// CreateOrder inserts the order and all its items in one transaction.
func (s *Store) CreateOrder(ctx context.Context, customerID int64, items []Item) (int64, error) {
	skus := make([]string, len(items))
	qtys := make([]int32, len(items))
	for i, it := range items {
		skus[i] = it.SKU
		qtys[i] = it.Qty
	}

	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO orders (customer_id) VALUES ($1) RETURNING id`,
			customerID,
		).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO order_items (order_id, sku, qty)
			 SELECT $1, t.sku, t.qty FROM unnest($2::text[], $3::int[]) AS t(sku, qty)`,
			id, skus, qtys,
		)
		return err
	})
	return id, err
}

// GetOrder returns the order with its items, or errNotFound.
func (s *Store) GetOrder(ctx context.Context, id int64) (*Order, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT o.id, o.customer_id, o.status, o.created_at, i.sku, i.qty
		 FROM orders o
		 LEFT JOIN order_items i ON i.order_id = o.id
		 WHERE o.id = $1
		 ORDER BY i.sku`,
		id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var o *Order
	for rows.Next() {
		var (
			row Order
			sku *string
			qty *int32
		)
		if err := rows.Scan(&row.ID, &row.CustomerID, &row.Status, &row.CreatedAt, &sku, &qty); err != nil {
			return nil, err
		}
		if o == nil {
			row.CreatedAt = row.CreatedAt.UTC()
			row.Items = []Item{}
			o = &row
		}
		if sku != nil && qty != nil {
			o.Items = append(o.Items, Item{SKU: *sku, Qty: *qty})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errNotFound
	}
	return o, nil
}

// ListCustomerOrders returns up to limit orders older than after (or the
// newest if after is nil), each with its items, plus the cursor for the next
// page (nil on the last page). It uses two queries regardless of page size.
func (s *Store) ListCustomerOrders(ctx context.Context, customerID int64, after *Cursor, limit int) ([]Order, *Cursor, error) {
	var (
		rows pgx.Rows
		err  error
	)
	// Fetch one extra row to learn whether another page exists.
	if after == nil {
		rows, err = s.pool.Query(ctx,
			`SELECT id, customer_id, status, created_at FROM orders
			 WHERE customer_id = $1
			 ORDER BY created_at DESC, id DESC
			 LIMIT $2`,
			customerID, limit+1,
		)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id, customer_id, status, created_at FROM orders
			 WHERE customer_id = $1 AND (created_at, id) < ($2, $3)
			 ORDER BY created_at DESC, id DESC
			 LIMIT $4`,
			customerID, after.CreatedAt, after.ID, limit+1,
		)
	}
	if err != nil {
		return nil, nil, err
	}
	orders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Order, error) {
		var o Order
		err := row.Scan(&o.ID, &o.CustomerID, &o.Status, &o.CreatedAt)
		o.CreatedAt = o.CreatedAt.UTC()
		o.Items = []Item{}
		return o, err
	})
	if err != nil {
		return nil, nil, err
	}

	var next *Cursor
	if len(orders) > limit {
		orders = orders[:limit]
		last := orders[limit-1]
		next = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if len(orders) == 0 {
		return orders, nil, nil
	}

	ids := make([]int64, len(orders))
	pos := make(map[int64]int, len(orders))
	for i, o := range orders {
		ids[i] = o.ID
		pos[o.ID] = i
	}
	itemRows, err := s.pool.Query(ctx,
		`SELECT order_id, sku, qty FROM order_items
		 WHERE order_id = ANY($1)
		 ORDER BY order_id, sku`,
		ids,
	)
	if err != nil {
		return nil, nil, err
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var (
			orderID int64
			it      Item
		)
		if err := itemRows.Scan(&orderID, &it.SKU, &it.Qty); err != nil {
			return nil, nil, err
		}
		if i, ok := pos[orderID]; ok {
			orders[i].Items = append(orders[i].Items, it)
		}
	}
	if err := itemRows.Err(); err != nil {
		return nil, nil, err
	}
	return orders, next, nil
}
