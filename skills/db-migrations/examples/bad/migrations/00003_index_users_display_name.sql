-- MISTAKE: no -- +goose NO TRANSACTION. goose wraps the file in a transaction and
-- Postgres rejects it: CREATE INDEX CONCURRENTLY cannot run inside a transaction block.
-- +goose Up
CREATE INDEX CONCURRENTLY users_display_name_idx ON users (display_name);

-- +goose Down
DROP INDEX CONCURRENTLY users_display_name_idx;
