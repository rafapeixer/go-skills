-- Contract: ships one release after 00002, once no running version reads
-- or writes users.name and the backfill job has filled display_name.
-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE users DROP COLUMN name;

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE users ADD COLUMN name text;
