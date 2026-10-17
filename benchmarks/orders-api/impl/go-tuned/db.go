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

const (
	// The RDS instance allows 80 connections for this service. Divide by the
	// most replicas that can run at once: 8 plus a rolling-deploy surge of 2.
	dbConnBudget = 80
	maxReplicas  = 10

	maxConnLifetime = 30 * time.Minute
	acquireWait     = 250 * time.Millisecond

	// Server-side backstops for a pod that dies mid-query or mid-transaction;
	// above the request deadline so the context normally fires first.
	statementTimeout   = "9000"  // ms
	idleInTxTimeout    = "10000" // ms
	defaultDialTimeout = 5 * time.Second
)

var (
	errPoolExhausted = errors.New("database pool exhausted")
	errNotFound      = errors.New("not found")
)

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

// orderRow is one order with its items aggregated into two parallel arrays
// (same length, same order), so a page of orders is one round trip.
type orderRow struct {
	ID         int64     `db:"id"`
	CustomerID int64     `db:"customer_id"`
	Status     string    `db:"status"`
	CreatedAt  time.Time `db:"created_at"`
	SKUs       []string  `db:"skus"`
	Qtys       []int32   `db:"qtys"`
}

func (r orderRow) toOrder() (Order, error) {
	if len(r.SKUs) != len(r.Qtys) {
		return Order{}, fmt.Errorf("order %d: %d skus but %d quantities", r.ID, len(r.SKUs), len(r.Qtys))
	}
	items := make([]Item, len(r.SKUs))
	for i := range r.SKUs {
		items[i] = Item{SKU: r.SKUs[i], Qty: r.Qtys[i]}
	}
	return Order{ID: r.ID, CustomerID: r.CustomerID, Status: r.Status, CreatedAt: r.CreatedAt.UTC(), Items: items}, nil
}

// cursor is the keyset position of the last order on a page.
type cursor struct {
	createdAt time.Time
	id        int64
}

func (c cursor) encode() string {
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d.%d", c.createdAt.UnixMicro(), c.id))
}

func decodeCursor(s string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, err
	}
	micros, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return cursor{}, errors.New("malformed cursor")
	}
	m, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return cursor{}, err
	}
	i, err := strconv.ParseInt(id, 10, 64)
	if err != nil || i <= 0 {
		return cursor{}, errors.New("malformed cursor")
	}
	return cursor{createdAt: time.UnixMicro(m).UTC(), id: i}, nil
}

type store struct {
	pool *pgxpool.Pool
}

func newPoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if cfg.ConnConfig.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
		return nil, errors.New("parse DATABASE_URL: simple_protocol is not allowed; use exec behind poolers")
	}
	// Set in code so a pool_max_conns in the DSN cannot exceed the budget.
	cfg.MaxConns = dbConnBudget / maxReplicas
	cfg.MinConns = 0
	cfg.MaxConnLifetime = maxConnLifetime
	cfg.MaxConnLifetimeJitter = maxConnLifetime / 10
	cfg.PingTimeout = time.Second
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = defaultDialTimeout
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	rp := cfg.ConnConfig.RuntimeParams
	if _, ok := rp["statement_timeout"]; !ok {
		rp["statement_timeout"] = statementTimeout
	}
	if _, ok := rp["idle_in_transaction_session_timeout"]; !ok {
		rp["idle_in_transaction_session_timeout"] = idleInTxTimeout
	}
	return cfg, nil
}

func newStore(ctx context.Context, dsn string) (*store, error) {
	cfg, err := newPoolConfig(dsn)
	if err != nil {
		return nil, err
	}
	// Connections are opened lazily, so a database blip at startup does not
	// crash-loop the container.
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	return &store{pool: pool}, nil
}

func (s *store) close() { s.pool.Close() }

// withConn acquires a connection with a short wait and reports
// errPoolExhausted when none frees up, so the caller can shed load.
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

// The order and its items are inserted in one statement, which is atomic
// without an explicit transaction.
const insertOrderSQL = `
WITH o AS (
  INSERT INTO orders (customer_id) VALUES ($1) RETURNING id
), i AS (
  INSERT INTO order_items (order_id, sku, qty)
  SELECT o.id, t.sku, t.qty FROM o, unnest($2::text[], $3::int[]) AS t(sku, qty)
)
SELECT id FROM o`

func (s *store) createOrder(ctx context.Context, customerID int64, items []Item) (int64, error) {
	skus := make([]string, len(items))
	qtys := make([]int32, len(items))
	for i, it := range items {
		skus[i], qtys[i] = it.SKU, it.Qty
	}
	var id int64
	err := s.withConn(ctx, func(c *pgxpool.Conn) error {
		if err := c.QueryRow(ctx, insertOrderSQL, customerID, skus, qtys).Scan(&id); err != nil {
			return fmt.Errorf("insert order: %w", err)
		}
		return nil
	})
	return id, err
}

// Every read returns the order and its items in one round trip: the LATERAL
// subquery is one index scan of order_items (order_id, sku) per order, and the
// two arrays are decoded in binary without a second query or a JSON parse.
// The SQL text is assembled from constants only; every value is a parameter.
const (
	selectOrdersSQL = `
SELECT o.id, o.customer_id, o.status, o.created_at,
       coalesce(i.skus, '{}') AS skus, coalesce(i.qtys, '{}') AS qtys
FROM `
	withItemsSQL = ` o
LEFT JOIN LATERAL (
  SELECT array_agg(sku ORDER BY sku) AS skus, array_agg(qty ORDER BY sku) AS qtys
  FROM order_items WHERE order_id = o.id
) i ON true`

	selectOrderSQL = selectOrdersSQL + `orders` + withItemsSQL + ` WHERE o.id = $1`
)

func (s *store) getOrder(ctx context.Context, id int64) (Order, error) {
	var o Order
	err := s.withConn(ctx, func(c *pgxpool.Conn) error {
		rows, _ := c.Query(ctx, selectOrderSQL, id)
		row, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[orderRow])
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("select order: %w", err)
		}
		o, err = row.toOrder()
		return err
	})
	return o, err
}

// Both page queries are range scans of orders_customer_created; the row
// comparison matches its (created_at DESC, id DESC) order. The LIMIT runs in
// the subquery, so the LATERAL item lookup happens only for the page's rows.
const (
	listOrdersFirstSQL = selectOrdersSQL + `(
  SELECT id, customer_id, status, created_at FROM orders
  WHERE customer_id = $1
  ORDER BY created_at DESC, id DESC
  LIMIT $2)` + withItemsSQL + ` ORDER BY o.created_at DESC, o.id DESC`
	listOrdersAfterSQL = selectOrdersSQL + `(
  SELECT id, customer_id, status, created_at FROM orders
  WHERE customer_id = $1 AND (created_at, id) < ($3, $4)
  ORDER BY created_at DESC, id DESC
  LIMIT $2)` + withItemsSQL + ` ORDER BY o.created_at DESC, o.id DESC`
)

// listCustomerOrders returns up to limit orders older than after (or the
// newest when after is nil) and the cursor of the next page, if any.
func (s *store) listCustomerOrders(ctx context.Context, customerID int64, limit int, after *cursor) ([]Order, *cursor, error) {
	var (
		orders []Order
		next   *cursor
	)
	err := s.withConn(ctx, func(c *pgxpool.Conn) error {
		var rows pgx.Rows
		if after == nil {
			rows, _ = c.Query(ctx, listOrdersFirstSQL, customerID, limit+1)
		} else {
			rows, _ = c.Query(ctx, listOrdersAfterSQL, customerID, limit+1, after.createdAt, after.id)
		}
		page, err := pgx.CollectRows(rows, pgx.RowToStructByName[orderRow])
		if err != nil {
			return fmt.Errorf("select customer orders: %w", err)
		}
		if len(page) > limit {
			page = page[:limit]
			last := page[len(page)-1]
			next = &cursor{createdAt: last.CreatedAt, id: last.ID}
		}
		orders = make([]Order, len(page))
		for i, r := range page {
			if orders[i], err = r.toOrder(); err != nil {
				return err
			}
		}
		return nil
	})
	return orders, next, err
}
