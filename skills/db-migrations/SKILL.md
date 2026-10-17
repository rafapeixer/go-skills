---
name: db-migrations
description: Use when adding or reviewing Postgres schema migrations in a Go service, choosing a migration tool, deciding where and when migrations run during a deploy, or changing a table that live traffic uses. Covers goose v3 with embedded SQL files and the Provider API, running migrations as a separate Job instead of on every replica's startup, expand/contract across releases, lock_timeout in every migration, CREATE INDEX CONCURRENTLY with NO TRANSACTION, SELECT * and cached plans after DDL, and testing migration conventions without a database.
---

# Database migrations

## The decision

Every schema change in a Go service needs a tool that applies it, a place in the deploy where it runs, and a shape that keeps the running service working while it happens. The failures are rarely in the SQL itself: ten replicas racing for the same `ALTER` on startup, a rollout stuck in `CrashLoopBackOff` because a migration outlived the liveness probe, an `ALTER TABLE` waiting for a lock while every query on the table queues behind it, a rename that breaks the pods of the previous release that are still serving, an index build that fails because it ran inside a transaction. This skill fixes the tool (goose v3), where migrations run (a separate step, before the rollout) and how each migration is written so it is safe on a table under load. How the service's pool is configured is [`postgres-pgx-pool`](../postgres-pgx-pool/SKILL.md); how queries and transactions are written is `sql-queries-and-transactions`.

## The rule

1. Write migrations as goose v3 SQL files embedded with `//go:embed` and apply them through `goose.NewProvider` with a Postgres locker, not through the package-level `goose.Up` API.
2. Run migrations from an explicit command (a Kubernetes Job, a pre-upgrade hook or a pipeline step) that connects directly to Postgres and finishes before the rollout, never from the service's startup in every replica.
3. Keep every release working against both the schema before and the schema after its migrations: expand in one release, backfill in batches, and contract only in a later release.
4. Set `lock_timeout` at the start of every Up and Down section (`SET LOCAL` in a transactional file, `SET` and `RESET` in a `NO TRANSACTION` file), and let the job retry when it fires.
5. Build indexes on existing tables with `CREATE INDEX CONCURRENTLY` in a file marked `-- +goose NO TRANSACTION`, preceded by `DROP INDEX CONCURRENTLY IF EXISTS` so a failed build can be retried.
6. Name every column in application queries, never `SELECT *`, and after DDL that changes a result type behind PgBouncer, run `RECONNECT` on its admin console.
7. Check the conventions in a unit test that walks the embedded files and builds the provider without connecting to a database.

## Why

**Rule 1.** goose covers what a Go service needs without a separate toolchain: plain SQL files with `-- +goose Up` and `-- +goose Down` sections, Go migrations when SQL is not enough, an `fs.FS` source so the files ship inside the binary, and Postgres locking. Use the latest release, v3.29.0 today; v3.28.0 and later require Go 1.26, which matches this repository's floor. Embedding means the Job runs exactly the SQL that was reviewed and built into this release, with no volume mount or image path to drift. The `Provider` API (`goose.NewProvider(goose.DialectPostgres, db, fsys, opts...)`) is the one to use: it holds its own configuration instead of package globals (`SetBaseFS`, `SetDialect`), takes a `context.Context` on `Up`, returns a `[]*MigrationResult` you can log, and accepts a locker. The package-level `goose.Up(db, dir)` runs with `context.Background()` and takes no lock, so nothing bounds it and nothing stops two runners from applying the same version. `lock.NewPostgresSessionLocker()` takes a session-level advisory lock on the connection goose holds for the whole run; `lock.NewPostgresTableLocker()` (since v3.26.0) keeps a lease row instead, for setups where a session lock is not available. goose needs a `*sql.DB`, so open one with the pgx `stdlib` driver for the migration command; it is a short-lived process, not the service's pool.

**Rule 2.** Migrating in `main` before `ListenAndServe` looks harmless with one replica. With ten, every pod starts the same migration at once: without a lock they race on the same DDL, and with a lock nine pods sit blocked while the first one works. A migration that takes longer than the startup or liveness probe gets the pod killed mid-run, the next pod starts it again, and the rollout loops. The service process also needs DDL privileges it should not have. A Job (or the pipeline step, or a Helm `pre-upgrade` hook) runs once, with its own timeout, credentials and logs, and the rollout starts only when it succeeds; a failure stops the deploy instead of half of the pods. Point the migration command at Postgres directly or through a session-mode pool, not at the service's transaction pooler: in transaction pooling PgBouncer may give each statement a different server connection, so a session `SET lock_timeout` or an advisory lock is lost between statements, or leaks into another client's session.

**Rule 3.** During a rolling update the pods of the previous release keep serving after the migration has run, and a rollback puts them back. A one-step `RENAME COLUMN`, `DROP COLUMN` or type change breaks every query of the old code the moment it commits. Split the change: the expand release adds the new column (nullable, or with a non-volatile default, which Postgres stores in the catalog without rewriting the table) and new code writes both columns; a backfill fills old rows in small batches, outside the migration's transaction, so it never holds row locks on the whole table at once; the next release reads only the new column; the release after that drops the old one. For constraints, add them `NOT VALID` and run `VALIDATE CONSTRAINT` in a later migration, which takes only a `SHARE UPDATE EXCLUSIVE` lock; for `SET NOT NULL`, a valid `CHECK (col IS NOT NULL)` lets Postgres skip the full scan. A volatile default, a stored generated column or most column type changes rewrite the whole table and its indexes under an `ACCESS EXCLUSIVE` lock; do them as a new column plus a backfill instead. The cost is real: one logical change becomes two or three releases, and the code carries dual writes for a while.

**Rule 4.** Many forms of `ALTER TABLE` take an `ACCESS EXCLUSIVE` lock, which conflicts with every other lock, including the `ACCESS SHARE` of a plain `SELECT`. If one long transaction or a slow report holds any lock on the table, the `ALTER` waits, and the queries that arrive after it queue behind the waiting `ALTER`: a change that runs in milliseconds turns into an outage that lasts as long as the slowest open transaction. `lock_timeout` defaults to 0, which means wait forever. With `SET LOCAL lock_timeout = '5s'`, the `ALTER` gives up after five seconds, the transaction rolls back, traffic resumes, and the Job retries later. `SET LOCAL` lasts until the end of the transaction, which is why both sections need it: goose runs Up and Down as separate transactions. In a `NO TRANSACTION` file `SET LOCAL` has no effect, so use `SET` and `RESET` at the end, because goose runs every migration of a run on the same connection. Keep `lock_timeout` well below any `statement_timeout`; when the statement timeout is the same or smaller, it always fires first.

**Rule 5.** A plain `CREATE INDEX` locks out writes on the table until the build ends, which is minutes on a large table. `CONCURRENTLY` builds without blocking inserts, updates or deletes, but it cannot run inside a transaction block, and goose wraps every file in one unless the file has `-- +goose NO TRANSACTION`. Without it the migration fails on every run. A concurrent build that fails (a deadlock, a uniqueness violation, a `lock_timeout`) leaves an `INVALID` index that queries ignore but writes still maintain; Postgres recommends dropping it and building again. `DROP INDEX CONCURRENTLY IF EXISTS` first makes the migration safe to rerun. Do not use `CREATE INDEX CONCURRENTLY IF NOT EXISTS` for this: it skips the build when an index with that name exists, even an invalid one. Put one index per `NO TRANSACTION` file, because nothing rolls back the statements that already ran when a later one fails.

**Rule 6.** pgx prepares and caches statements per connection by default (see [`postgres-pgx-pool`](../postgres-pgx-pool/SKILL.md), rule 5). Postgres re-plans a prepared statement after DDL on the objects it uses, but a statement whose result row type changes fails with `cached plan must not change result type`. `SELECT *` changes its result type whenever a column is added or dropped, so an expand migration that is safe in every other way breaks reads in every running pod; with named columns, adding a column changes nothing, and the code that scans by position does not break either. pgx drops a cached statement after it fails, so on a direct connection the error clears on the next call on that connection. Behind PgBouncer with `max_prepared_statements` enabled, PgBouncer's documentation names DDL migrations that add a column or change a column type as the common cause and says to run `RECONNECT` on the admin console after the migration. Add that step to the migration Job when a change alters a result type.

**Rule 7.** goose ignores files whose name does not start with a number and an underscore: `add_index.sql` or `0005-add-index.sql` is not an error, it is a migration that never runs. A test that builds the provider over the embedded `fs.FS` and compares `ListSources()` with the files on disk catches that, and checking the files for `lock_timeout` in both sections and for `CONCURRENTLY` without `NO TRANSACTION` turns rules 4 and 5 into CI failures instead of review comments. `sql.Open` does not connect and `goose.NewProvider` only lists files, so the test needs no database. Run the migrations themselves against a real Postgres in integration tests (see [`testing`](../testing/SKILL.md)).

## Good example

See [`examples/good/`](examples/good/).

```go
func newProvider(db *sql.DB) (*goose.Provider, error) {
	fsys, err := fs.Sub(embedded, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	// A session advisory lock makes a second runner wait instead of racing
	// the first. It needs a direct (or session-pooled) connection.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("create migration locker: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
```

```sql
-- +goose NO TRANSACTION
-- CONCURRENTLY cannot run inside a transaction block. A failed build leaves an
-- INVALID index behind, so drop it first and the migration is safe to retry.
-- +goose Up
SET lock_timeout = '5s';
DROP INDEX CONCURRENTLY IF EXISTS users_display_name_idx;
CREATE INDEX CONCURRENTLY users_display_name_idx ON users (display_name);
RESET lock_timeout;
```

`main.go` is a `migrate` command that reads `MIGRATE_DATABASE_URL`, bounds the run with a timeout and exits non-zero on failure, so a Job can run it before the rollout. The four migrations rename `users.name` to `display_name` by expand (`00002` adds a nullable column), index (`00003`) and contract (`00004`, shipped a release later). `migrations_test.go` checks file naming, sequential versions, `lock_timeout` in every section and `CONCURRENTLY` only with `NO TRANSACTION`, and that the app query names its columns; none of it needs a database.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: every replica migrates on startup, with no locker. Ten pods race
	// for the same DDL, and a slow migration fails liveness probes, so the
	// rollout loops in CrashLoopBackOff with the table locked.
	// MISTAKE: package-level goose state (SetBaseFS, SetDialect) and no timeout:
	// goose.Up uses context.Background, so a blocked ALTER hangs startup forever.
	goose.SetBaseFS(embedMigrations)
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatal(err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		log.Fatal(err)
	}
```

```sql
-- MISTAKE: no -- +goose NO TRANSACTION. goose wraps the file in a transaction and
-- Postgres rejects it: CREATE INDEX CONCURRENTLY cannot run inside a transaction block.
-- +goose Up
CREATE INDEX CONCURRENTLY users_display_name_idx ON users (display_name);
```

The other files hold the rest of the usual mistakes: `00002` renames a column in one step with no `lock_timeout`, `add_users_email_unique.sql` has no version prefix and is silently skipped, and the handler reads with `SELECT *` and scans by position. All of it works on a laptop with one replica and an empty table, which is why it reaches production.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Migrations are goose v3 SQL files embedded with `//go:embed` and applied through `goose.NewProvider` with a Postgres locker, not the package-level `goose.Up`.
- [ ] Migrations run from a separate command or Job before the rollout, and the service's startup path does not call goose.
- [ ] The migration command connects to Postgres directly or through session pooling, and its run is bounded by a timeout.
- [ ] No migration renames, drops or changes the type of something the currently deployed release still reads or writes.
- [ ] Every Up and Down section sets `lock_timeout` (`SET LOCAL` with a transaction, `SET` and `RESET` without).
- [ ] Every `CREATE INDEX` on an existing table uses `CONCURRENTLY`, sits alone in a `-- +goose NO TRANSACTION` file and drops a leftover index first.
- [ ] Large backfills run in batches outside the DDL transaction, not as one `UPDATE` inside a migration.
- [ ] Application queries name their columns; no `SELECT *` in code that runs against a table migrations change.
- [ ] A test without a database checks that goose lists every migration file and that the files follow these conventions.

## When not to apply

- **One process owns the database.** A single-instance tool or a service that never runs more than one replica and has no rolling update can migrate on startup with `provider.Up` under a deadline; there is nothing to race and no old version still serving. Keep the lock and `lock_timeout`; they cost nothing.
- **Tables nobody reads yet.** A table created in the same release has no traffic and no old code: create it and its indexes in one transactional file without `CONCURRENTLY` or expand/contract. The `lock_timeout` rule still applies, because the file may also touch existing tables.
- **A planned maintenance window.** If the service is stopped and downtime was agreed, a one-step rename or a table rewrite is acceptable; say so in the migration file, so nobody copies it as a pattern.
- **Integration tests.** Applying the embedded migrations with `provider.Up` in test setup, or through a template database, is the right way to get a real schema; the separate Job rule is about production rollouts.
- **A team already on another tool.** Atlas (declarative schema with migration linting) or golang-migrate are reasonable if they are already in place and wired into CI; do not switch only for this skill. Rules 2 to 6 apply to any tool: how they run, how they lock and how they change tables is the part that causes outages.
- **Changes SQL cannot express.** A transformation that needs Go code can be a goose Go migration registered with `goose.WithGoMigrations`, but a backfill over millions of rows belongs in a batched, resumable job, not in a migration that the deploy waits on.
