-- +goose Up
-- MISTAKE: no lock_timeout. If a long transaction holds any lock on users, this
-- ALTER waits for ACCESS EXCLUSIVE, and every query on users queues behind it.
-- MISTAKE: a rename in one step. Pods of the previous release still run
-- during the rollout and fail on every query that names users.name.
ALTER TABLE users RENAME COLUMN name TO display_name;

-- +goose Down
ALTER TABLE users RENAME COLUMN display_name TO name;
