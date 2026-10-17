-- +goose Up
SET LOCAL lock_timeout = '5s';
CREATE TABLE users (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email      text NOT NULL,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP TABLE users;
