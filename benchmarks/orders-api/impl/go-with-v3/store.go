package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errNotFound      = errors.New("not found")
	errPoolExhausted = errors.New("database pool exhausted")
	errBadCursor     = errors.New("invalid cursor")
)

// acquireWait bounds how long a request waits for a free connection before
// it is shed with 503, well under requestTimeout.
const acquireWait = 200 * time.Millisecond

type item struct {
	SKU string `json:"sku"`
	Qty int32  `json:"qty"`
}

type order struct {
	ID         int64     `json:"id"`
	CustomerID int64     `json:"customer_id"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	Items      []item    `json:"items"`
}

// cursor is the keyset position of the last order on a page.
type cursor struct {
	createdAt time.Time
	id        int64
}

func (c cursor) encode() string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(c.createdAt.UnixMicro()))
	binary.BigEndian.PutUint64(b[8:], uint64(c.id))
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func decodeCursor(s string) (cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != 16 {
		return cursor{}, errBadCursor
	}
	return cursor{
		createdAt: time.UnixMicro(int64(binary.BigEndian.Uint64(b[:8]))),
		id:        int64(binary.BigEndian.Uint64(b[8:])),
	}, nil
}

// Items per order are few (at most 100), so they come back with their order
// in the same statement as parallel arrays.
const (
	orderColumns = `o.id, o.customer_id, o.status, o.created_at,
	coalesce(i.skus, '{}'::text[]), coalesce(i.qtys, '{}'::int[])`
	itemsLateral = `
LEFT JOIN LATERAL (
	SELECT array_agg(sku ORDER BY sku) AS skus, array_agg(qty ORDER BY sku) AS qtys
	FROM order_items WHERE order_id = o.id
) i ON true`

	createOrderSQL = `
WITH o AS (
	INSERT INTO orders (customer_id) VALUES ($1) RETURNING id
), i AS (
	INSERT INTO order_items (order_id, sku, qty)
	SELECT o.id, t.sku, t.qty FROM o, unnest($2::text[], $3::int[]) AS t(sku, qty)
)
SELECT id FROM o`

	getOrderSQL = `SELECT ` + orderColumns + ` FROM orders o` + itemsLateral + ` WHERE o.id = $1`

	// The keyset predicate matches orders_customer_created, so every page is
	// an index range scan however deep it is. LIMIT sits in the subquery so
	// items are loaded only for the rows of the page.
	listFirstPageSQL = `SELECT ` + orderColumns + ` FROM (
	SELECT id, customer_id, status, created_at FROM orders
	WHERE customer_id = $1
	ORDER BY created_at DESC, id DESC
	LIMIT $2
) o` + itemsLateral + `
ORDER BY o.created_at DESC, o.id DESC`

	listNextPageSQL = `SELECT ` + orderColumns + ` FROM (
	SELECT id, customer_id, status, created_at FROM orders
	WHERE customer_id = $1 AND (created_at, id) < ($3, $4)
	ORDER BY created_at DESC, id DESC
	LIMIT $2
) o` + itemsLateral + `
ORDER BY o.created_at DESC, o.id DESC`
)

type store struct {
	pool *pgxpool.Pool
}

// withConn acquires a connection with a short deadline so a saturated pool
// sheds load instead of queueing requests for their whole deadline.
func (s *store) withConn(ctx context.Context, fn func(*pgxpool.Conn) error) error {
	actx, cancel := context.WithTimeout(ctx, acquireWait)
	conn, err := s.pool.Acquire(actx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return errPoolExhausted
		}
		return fmt.Errorf("acquire database connection: %w", err)
	}
	defer conn.Release()
	return fn(conn)
}

func (s *store) ping(ctx context.Context) error {
	return s.withConn(ctx, func(c *pgxpool.Conn) error { return c.Ping(ctx) })
}

// createOrder inserts the order and its items in one statement, which is
// atomic on its own.
func (s *store) createOrder(ctx context.Context, customerID int64, items []item) (int64, error) {
	skus := make([]string, len(items))
	qtys := make([]int32, len(items))
	for i, it := range items {
		skus[i], qtys[i] = it.SKU, it.Qty
	}
	var id int64
	err := s.withConn(ctx, func(c *pgxpool.Conn) error {
		return c.QueryRow(ctx, createOrderSQL, customerID, skus, qtys).Scan(&id)
	})
	if err != nil {
		return 0, fmt.Errorf("create order: %w", err)
	}
	return id, nil
}

func (s *store) getOrder(ctx context.Context, id int64) (order, error) {
	var o order
	err := s.withConn(ctx, func(c *pgxpool.Conn) error {
		var err error
		o, err = scanOrder(c.QueryRow(ctx, getOrderSQL, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return order{}, errNotFound
	}
	if err != nil {
		return order{}, fmt.Errorf("get order %d: %w", id, err)
	}
	return o, nil
}

// listOrders returns up to limit orders older than after (nil for the first
// page), newest first, and the cursor of the next page if there is one.
func (s *store) listOrders(ctx context.Context, customerID int64, limit int, after *cursor) ([]order, *cursor, error) {
	orders := make([]order, 0, limit+1)
	err := s.withConn(ctx, func(c *pgxpool.Conn) error {
		var (
			rows pgx.Rows
			err  error
		)
		// Fetch one extra row to learn whether another page exists.
		if after == nil {
			rows, err = c.Query(ctx, listFirstPageSQL, customerID, limit+1)
		} else {
			rows, err = c.Query(ctx, listNextPageSQL, customerID, limit+1, after.createdAt, after.id)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOrder(rows)
			if err != nil {
				return err
			}
			orders = append(orders, o)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list orders of customer %d: %w", customerID, err)
	}
	if len(orders) <= limit {
		return orders, nil, nil
	}
	orders = orders[:limit]
	last := orders[limit-1]
	return orders, &cursor{createdAt: last.CreatedAt, id: last.ID}, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(row scanner) (order, error) {
	var (
		o    order
		skus []string
		qtys []int32
	)
	if err := row.Scan(&o.ID, &o.CustomerID, &o.Status, &o.CreatedAt, &skus, &qtys); err != nil {
		return order{}, err
	}
	if len(skus) != len(qtys) {
		return order{}, fmt.Errorf("order %d: %d skus but %d quantities", o.ID, len(skus), len(qtys))
	}
	o.CreatedAt = o.CreatedAt.UTC()
	o.Items = make([]item, len(skus))
	for i := range skus {
		o.Items[i] = item{SKU: skus[i], Qty: qtys[i]}
	}
	return o, nil
}
