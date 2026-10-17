package main

// Like db.go, this stands in for what `sqlc generate` writes, here from the
// OrderPageWithItems query in query.sql: a page of orders and their items in
// one round trip, the items as two parallel arrays decoded in binary.

import (
	"context"
	"time"
)

const orderPageWithItems = `-- name: OrderPageWithItems :many
SELECT o.id, o.customer_id, o.total_cents, o.created_at,
       coalesce(i.skus, '{}')::text[] AS skus, coalesce(i.qtys, '{}')::int[] AS qtys
FROM (
    SELECT id, customer_id, total_cents, created_at FROM orders
    WHERE customer_id = $1
    ORDER BY created_at DESC, id DESC
    LIMIT $2
) o
LEFT JOIN LATERAL (
    SELECT array_agg(sku ORDER BY sku) AS skus, array_agg(qty ORDER BY sku) AS qtys
    FROM order_items WHERE order_id = o.id
) i ON true
ORDER BY o.created_at DESC, o.id DESC
`

type OrderPageWithItemsParams struct {
	CustomerID int64 `db:"customer_id"`
	Limit      int32 `db:"limit"`
}

type OrderPageWithItemsRow struct {
	ID         int64     `db:"id"`
	CustomerID int64     `db:"customer_id"`
	TotalCents int64     `db:"total_cents"`
	CreatedAt  time.Time `db:"created_at"`
	Skus       []string  `db:"skus"`
	Qtys       []int32   `db:"qtys"`
}

func (q *Queries) OrderPageWithItems(ctx context.Context, arg OrderPageWithItemsParams) ([]OrderPageWithItemsRow, error) {
	rows, err := q.db.Query(ctx, orderPageWithItems, arg.CustomerID, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []OrderPageWithItemsRow
	for rows.Next() {
		var i OrderPageWithItemsRow
		if err := rows.Scan(&i.ID, &i.CustomerID, &i.TotalCents, &i.CreatedAt, &i.Skus, &i.Qtys); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}
