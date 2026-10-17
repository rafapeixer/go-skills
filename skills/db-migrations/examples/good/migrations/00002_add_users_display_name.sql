-- Expand: a nullable column without a default is a catalog-only change.
-- Old pods keep writing name; new pods write both and read display_name.
-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE users ADD COLUMN display_name text;

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE users DROP COLUMN display_name;
