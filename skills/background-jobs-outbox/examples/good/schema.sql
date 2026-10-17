-- Application tables, applied by the migrate step. River's own tables come
-- from `river migrate-up` (or rivermigrate) in the same step.
CREATE TABLE orders (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id bigint NOT NULL,
    total_cents bigint NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- One row per receipt actually sent: the worker's idempotency record.
CREATE TABLE receipts (
    order_id bigint PRIMARY KEY REFERENCES orders (id),
    sent_at  timestamptz NOT NULL DEFAULT now()
);

-- The durable queue for events relayed to a broker. A row is pending until
-- sent_at or dead_at is set; available_at doubles as retry time and lease.
CREATE TABLE outbox (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic        text NOT NULL,
    payload      jsonb NOT NULL,
    attempts     int NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    sent_at      timestamptz,
    dead_at      timestamptz,
    last_error   text
);

-- Only pending rows are indexed, so the claim stays an index scan however
-- many sent rows accumulate before cleanup deletes them.
CREATE INDEX outbox_pending ON outbox (available_at, id)
    WHERE sent_at IS NULL AND dead_at IS NULL;
