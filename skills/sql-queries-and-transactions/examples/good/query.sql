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
