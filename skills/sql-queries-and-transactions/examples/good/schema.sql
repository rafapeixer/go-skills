CREATE TABLE orders (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id bigint      NOT NULL,
    total_cents bigint      NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Serves the keyset page in both directions and the = ANY lookup.
CREATE INDEX orders_customer_created_id ON orders (customer_id, created_at, id);

-- Side effects are rows written in the same transaction; a relay delivers
-- them after commit.
CREATE TABLE outbox (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic      text        NOT NULL,
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
