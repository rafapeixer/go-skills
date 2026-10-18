package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// acquireWait bounds how long a request waits for a pooled connection before
// it is shed with 503; it is far below the request deadline on purpose.
const acquireWait = 200 * time.Millisecond

var (
	errPoolExhausted = errors.New("database pool exhausted")
	errNotFound      = errors.New("not found")
)

type item struct {
	SKU string `json:"sku"`
	Qty int64  `json:"qty"`
}

type order struct {
	ID         int64     `json:"id"`
	CustomerID int64     `json:"customer_id"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	Items      []item    `json:"items"`
}

// Items per order are few and bounded (at most 100), so they come back with
// their order in the same statement as parallel arrays.
const (
	orderColumns = `o.id, o.customer_id, o.status, o.created_at,
	coalesce(i.skus, '{}'), coalesce(i.qtys, '{}')`
	itemsLateral = `LEFT JOIN LATERAL (
	SELECT array_agg(sku ORDER BY sku) AS skus, array_agg(qty ORDER BY sku) AS qtys
	FROM order_items WHERE order_id = o.id
) i ON true`

	// One statement inserts the order and its items, so it is atomic without
	// an explicit transaction and costs a single round trip.
	createOrderSQL = `WITH o AS (
	INSERT INTO orders (customer_id) VALUES ($1) RETURNING id
), ins AS (
	INSERT INTO order_items (order_id, sku, qty)
	SELECT o.id, t.sku, t.qty FROM o, unnest($2::text[], $3::int[]) AS t(sku, qty)
)
SELECT id FROM o`

	getOrderSQL = `SELECT ` + orderColumns + ` FROM orders o ` + itemsLateral + ` WHERE o.id = $1`

	// Keyset pages on orders_customer_created (customer_id, created_at DESC, id DESC).
	// The LIMIT sits inside the subquery so items load only for the page's rows.
	listFirstPageSQL = `SELECT ` + orderColumns + ` FROM (
	SELECT id, customer_id, status, created_at FROM orders
	WHERE customer_id = $1
	ORDER BY created_at DESC, id DESC
	LIMIT $2
) o ` + itemsLateral + ` ORDER BY o.created_at DESC, o.id DESC`

	listNextPageSQL = `SELECT ` + orderColumns + ` FROM (
	SELECT id, customer_id, status, created_at FROM orders
	WHERE customer_id = $1 AND (created_at, id) < ($3::timestamptz, $4::bigint)
	ORDER BY created_at DESC, id DESC
	LIMIT $2
) o ` + itemsLateral + ` ORDER BY o.created_at DESC, o.id DESC`
)

type store struct {
	pool *pgxpool.Pool
}

// withConn acquires a connection with a short wait and returns
// errPoolExhausted when none frees up, so callers can shed with 503.
func (s *store) withConn(ctx context.Context, fn func(*pgxpool.Conn) error) error {
	actx, cancel := context.WithTimeout(ctx, acquireWait)
	conn, err := s.pool.Acquire(actx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return errPoolExhausted
		}
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()
	return fn(conn)
}

func (s *store) CreateOrder(ctx context.Context, customerID int64, items []item) (int64, error) {
	skus := make([]string, len(items))
	qtys := make([]int32, len(items))
	for i, it := range items {
		skus[i] = it.SKU
		qtys[i] = int32(it.Qty) // validated to fit by the handler
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

func (s *store) GetOrder(ctx context.Context, id int64) (order, error) {
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

// ListOrders returns up to limit orders older than after (or the newest when
// after is nil), and the cursor of the next page, nil on the last one.
func (s *store) ListOrders(ctx context.Context, customerID int64, limit int, after *cursor) ([]order, *cursor, error) {
	var orders []order
	err := s.withConn(ctx, func(c *pgxpool.Conn) error {
		var rows pgx.Rows
		var err error
		// One extra row tells whether another page exists.
		if after == nil {
			rows, err = c.Query(ctx, listFirstPageSQL, customerID, limit+1)
		} else {
			rows, err = c.Query(ctx, listNextPageSQL, customerID, limit+1, after.createdAt, after.id)
		}
		if err != nil {
			return err
		}
		orders, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (order, error) {
			return scanOrder(r)
		})
		return err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list orders of customer %d: %w", customerID, err)
	}
	if orders == nil {
		orders = []order{}
	}
	if len(orders) <= limit {
		return orders, nil, nil
	}
	orders = orders[:limit]
	last := orders[limit-1]
	return orders, &cursor{createdAt: last.CreatedAt, id: last.ID}, nil
}

func scanOrder(row pgx.Row) (order, error) {
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
		o.Items[i] = item{SKU: skus[i], Qty: int64(qtys[i])}
	}
	return o, nil
}

// cursor is the keyset position of the last order on a page. It is opaque to
// clients; created_at is kept in microseconds, Postgres's timestamp precision.
type cursor struct {
	createdAt time.Time
	id        int64
}

func (c cursor) encode() string {
	raw := strconv.FormatInt(c.createdAt.UnixMicro(), 10) + "." + strconv.FormatInt(c.id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	micros, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return cursor{}, errors.New("decode cursor: malformed")
	}
	us, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return cursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return cursor{}, errors.New("decode cursor: bad id")
	}
	return cursor{createdAt: time.UnixMicro(us).UTC(), id: n}, nil
}
