-- MISTAKE: no NUMBER_ prefix. goose skips this file without an error, so the
-- constraint never reaches any database and nobody notices.
-- +goose Up
ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);

-- +goose Down
ALTER TABLE users DROP CONSTRAINT users_email_key;
