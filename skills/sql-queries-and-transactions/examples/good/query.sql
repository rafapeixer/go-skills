-- name: OrdersByCustomers :many
SELECT id, customer_id, total_cents, created_at
FROM orders
WHERE customer_id = ANY(sqlc.arg(customer_ids)::bigint[])
ORDER BY customer_id, created_at DESC, id DESC;

-- name: InsertOrder :one
INSERT INTO orders (customer_id, total_cents)
VALUES ($1, $2)
RETURNING id;

-- name: InsertOutbox :exec
INSERT INTO outbox (topic, payload)
VALUES ($1, $2);

-- name: OrderPageWithItems :many
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
ORDER BY o.created_at DESC, o.id DESC;
