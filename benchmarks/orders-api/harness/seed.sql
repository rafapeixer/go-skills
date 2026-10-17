-- Schema from SPEC.md plus seed data: 1,000 customers and 200,000 orders with
-- 3 items each. Customer 1 is heavy (about 40,000 orders) so that list
-- endpoints that page with OFFSET or load items per order show their cost.
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

INSERT INTO orders (customer_id, created_at)
SELECT CASE WHEN g % 5 = 0 THEN 1 ELSE 1 + (g % 1000) END,
       now() - (g || ' seconds')::interval
FROM generate_series(1, 200000) AS g;

INSERT INTO order_items (order_id, sku, qty)
SELECT o.id, s.sku || (o.id % 500), 1 + (o.id % 3)
FROM orders o CROSS JOIN (VALUES ('A'), ('B'), ('C')) AS s(sku);

ANALYZE;
