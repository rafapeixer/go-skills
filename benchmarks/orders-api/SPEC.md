Build a production-ready Go HTTP service called `orders-api`. Write every file of the module in the current directory: `go.mod` (module `orders-api`, `go 1.26.0`), the Go source, and nothing else. It must build with `go build ./...` after `go mod tidy`.

## Deployment

- Runs in containers on AWS EC2 t4g.micro instances (2 vCPU, 1 GiB memory), **8 replicas** behind a load balancer.
- Postgres is a single RDS instance that allows **80 connections in total** for this service.
- Configuration comes from environment variables: `PORT` (default `8080`), `DATABASE_URL` (a Postgres URL), `PRICING_URL` (base URL of the pricing service).
- The container receives SIGTERM on deploys.

## Database

The schema already exists; do not create or migrate it.

```sql
CREATE TABLE orders (
  id          bigserial PRIMARY KEY,
  customer_id bigint      NOT NULL,
  status      text        NOT NULL DEFAULT 'new',
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX orders_customer_created ON orders (customer_id, created_at DESC, id DESC);

CREATE TABLE order_items (
  order_id bigint NOT NULL REFERENCES orders (id),
  sku      text   NOT NULL,
  qty      int    NOT NULL CHECK (qty > 0),
  PRIMARY KEY (order_id, sku)
);
```

## Pricing service

`GET {PRICING_URL}/prices/{sku}` returns `200 {"sku":"A1","cents":1250}` or `404` for an unknown SKU. It is another team's service: usually ~20 ms, sometimes much slower.

## API

All bodies are JSON. Errors return `{"error":"<message>"}`.

1. `POST /orders` with `{"customer_id":42,"items":[{"sku":"A1","qty":2}]}`
   - Creates the order and its items atomically. 1 to 100 items, `qty` ≥ 1, no duplicate SKUs, `customer_id` > 0.
   - `201` with `{"id":123}`. Invalid input: `400`.
2. `GET /orders/{id}`
   - `200` with `{"id":123,"customer_id":42,"status":"new","created_at":"<RFC 3339>","items":[{"sku":"A1","qty":2}]}`. Unknown id: `404`.
3. `GET /customers/{id}/orders?limit=20&cursor=<cursor>`
   - The customer's orders, newest first, each with its items (same shape as above), as `{"orders":[...],"next_cursor":"<cursor or empty>"}`.
   - `limit` defaults to 20, max 100. `next_cursor` is empty on the last page. Customers can have tens of thousands of orders.
4. `GET /orders/{id}/quote`
   - Looks up the price of every item from the pricing service and returns `{"order_id":123,"total_cents":4500}`. Unknown order: `404`. Any SKU the pricing service does not know: `422`. Pricing service failing or too slow: `503`.
5. `GET /healthz` returns `200`.
