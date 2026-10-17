-- +goose NO TRANSACTION
-- CONCURRENTLY cannot run inside a transaction block. A failed build leaves an
-- INVALID index behind, so drop it first and the migration is safe to retry.
-- +goose Up
SET lock_timeout = '5s';
DROP INDEX CONCURRENTLY IF EXISTS users_display_name_idx;
CREATE INDEX CONCURRENTLY users_display_name_idx ON users (display_name);
RESET lock_timeout;

-- +goose Down
SET lock_timeout = '5s';
DROP INDEX CONCURRENTLY IF EXISTS users_display_name_idx;
RESET lock_timeout;
