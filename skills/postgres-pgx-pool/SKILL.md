---
name: postgres-pgx-pool
description: Use when choosing a Postgres driver for a Go service, creating or configuring a connection pool, sizing MaxConns for several replicas, putting PgBouncer in front of Postgres, or handling pool exhaustion. Covers native pgxpool vs database/sql, the pgx version floor, MaxConns from the connection budget, lifetime jitter and ping timeout, bounded acquire with 503, QueryExecMode behind poolers, session state on direct connections, and the pgx 5.11 DSN parser.
metadata:
  verified: 2026-10-17
---

# Postgres pgx pool

## The decision

A Go service talks to Postgres through a pool, and the pool's settings decide how the service fails under load. Postgres caps connections with `max_connections` (100 by default), and every connection is a server process, so the number that matters is the total across every pod, not one pod's. Get the pool wrong and the failures show up at the worst moment: "too many clients" during an autoscaling event, a reconnect storm every hour, requests queued in memory while the database is already saturated, prepared statements that break behind PgBouncer, advisory locks that never release. This skill fixes which driver and pool you use and how you size and configure it. Writing queries and transactions, and running migrations, are separate decisions.

## The rule

1. Use `pgxpool.Pool` from `github.com/jackc/pgx/v5` at v5.9.2 or later, not `database/sql` with the pgx `stdlib` driver.
2. Build the pool from `pgxpool.ParseConfig` and set `MaxConns` in code to the service's connection budget divided by the maximum replica count including surge.
3. Set `MaxConnLifetimeJitter` (about 10% of `MaxConnLifetime`, at least one second) and `PingTimeout` explicitly, because both default to zero.
4. Acquire with a short deadline derived from the request and answer 503 when it expires, instead of raising `MaxConns` or waiting without a bound.
5. Keep the default `QueryExecModeCacheStatement`, never set `QueryExecModeSimpleProtocol` as the default, and behind a transaction pooler without prepared statement support use `QueryExecModeExec`.
6. Run `LISTEN`, session-level advisory locks and session `SET` on one dedicated connection that reaches Postgres directly or through session pooling.
7. When upgrading to pgx v5.11 or later, run every production DSN through `ParseConfig` in a test, because the parsers were rewritten to match libpq.

## Why

**Rule 1.** pgx is the one place this repository takes a third-party dependency for data access, because the standard library has no Postgres driver. Its native interface keeps what `database/sql` cannot express: `CopyFrom`, `LISTEN`/`NOTIFY`, `SendBatch`, binary encoding of Postgres types, and pool statistics such as `Stat().EmptyAcquireCount()`. Going through `database/sql` adds a second pool with its own `SetMaxOpenConns`, so a service that has both holds two budgets of connections. The version floor is v5.9.2: it fixes GHSA-j88v-2chj-qfwx, a SQL injection through dollar-quoted literals that affects only the simple protocol, and the release before it, v5.9.1, fixed batch result corruption with cached statements introduced in v5.9.0. Use the latest release (v5.11.0 today); v5.9.0 and later require Go 1.25, below this repository's floor. Use `pgx.ConnectConfig` without a pool only for a one-shot CLI or the dedicated connection in rule 6.

**Rule 2.** The `MaxConns` default is "the greater of 4 or `runtime.NumCPU()`". `NumCPU` counts the logical CPUs the process may run on, not the container's CPU limit, so a pod on a 64-CPU node gets 64 connections no matter how little CPU it is given, and twenty such pods ask for 1,280. Start from the database: take `max_connections`, subtract `superuser_reserved_connections` and what migrations, admin sessions and other services need, and that is the budget for this service. Divide it by the highest replica count you can reach, which is the autoscaler's maximum plus the rolling-update surge, not today's count. Set the result in code after `ParseConfig`, so a `pool_max_conns` left in some environment's DSN cannot silently override the budget. A smaller pool is usually faster as well: past roughly two connections per database core, more connections add contention, not throughput.

**Rule 3.** Every connection is closed after `MaxConnLifetime` (default one hour). Connections opened together at startup or after a failover expire together, and the pool reconnects all of them at once: TLS handshakes, authentication and cold statement caches in the same second, every hour. `MaxConnLifetimeJitter` spreads the expiry over a window; pgx computes the jitter in whole seconds, so a value under one second is zero. `PingTimeout` matters because the pool pings any connection idle for more than a second before handing it out, and with the default of zero that ping is bounded only by the caller's context: a half-dead TCP connection consumes the whole request deadline instead of being destroyed after a second while the pool tries the next one. Keep `MaxConnIdleTime` (30 minutes) and `HealthCheckPeriod` (one minute) at their defaults unless you have a measured reason. On pgx v5.11 and later a non-positive `MaxConnLifetime` means unlimited; before v5.11 it meant connections expired immediately.

**Rule 4.** When every connection is busy, `Acquire` waits until its context ends. With `context.Background()` it waits forever, and with only the request deadline every waiting request still holds a goroutine and its memory for the full timeout while the database, already the bottleneck, gets no relief. A full pool is the signal to shed load. Wait a short time (tens to a few hundred milliseconds, well under the request deadline), then return `503 Service Unavailable` so the load balancer and clients back off. pgx does not cancel a dial when the acquire deadline expires; the new connection finishes in the background and joins the pool. Raising `MaxConns` instead just moves the queue into Postgres, where each waiting query costs a server process and lock contention. Watch `EmptyAcquireCount` and `EmptyAcquireWaitTime` to see saturation before users do.

**Rule 5.** The default mode prepares each statement once per connection and then runs it in one round trip with binary encoding. `QueryExecModeSimpleProtocol` interpolates arguments into the SQL text on the client, which is where the v5.9.2 injection lived, and loses binary encoding; pgx's own docs say to prefer `QueryExecModeExec` whenever possible. Behind PgBouncer in transaction pooling mode, the default mode works on PgBouncer 1.21 and later when `max_prepared_statements` is above zero, which is the default (200) since PgBouncer 1.24. On an older PgBouncer, or with that setting at 0, set `default_query_exec_mode=exec` in the DSN; `cache_describe` is also compatible but fails on the first run after a schema change. Never use `QueryExecModeDescribeExec` with transaction pooling, because PgBouncer may switch server connections between its two round trips.

**Rule 6.** In transaction pooling, PgBouncer gives the client a server connection only for the length of a transaction, and its feature table marks `LISTEN`, session-level advisory locks, `SET`/`RESET`, SQL `PREPARE`, `WITH HOLD` cursors and non-`ON COMMIT DROP` temp tables as never supported. Even without PgBouncer, pgxpool has the same problem in miniature: `pool.Exec` returns the connection after each statement, so a `pg_advisory_lock` and its `pg_advisory_unlock` run on different connections and the lock leaks, and a `LISTEN` lands on a connection nobody waits on. Give the listener its own `pgx.ConnectConfig` connection (or `pool.Acquire` and hold it) pointed at Postgres or a session-mode pool, and use transaction-scoped `pg_advisory_xact_lock` inside a transaction when the lock only needs to cover one.

**Rule 7.** pgx v5.11.0 replaced its URI and keyword/value parsers with ones that match libpq exactly. Most DSNs are unaffected, but some change meaning or stop parsing: `+` in a query value is no longer a space, `#` is data, a repeated parameter takes the last value, `postgres://h1,h2:5433/db` now means h1 on 5432, and a backslash in a keyword/value string escapes the next character, so an unescaped Windows certificate path loses its separators. `ParseConfig` does not connect, so a table test over the DSN shapes you deploy costs nothing and catches the change in CI rather than at startup.

The cost: a fixed budget per pod means a single pod cannot burst past its share even when the database has room, and a short acquire wait turns some slow requests into 503s. That is the trade-off for a database that stays up while the service sheds load.

## Good example

See [`examples/good/`](examples/good/).

```go
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}
	if cfg.ConnConfig.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
		return nil, errors.New("parse database dsn: simple_protocol interpolates arguments client side; use exec behind poolers")
	}
	cfg.MaxConns = int32(budget / replicas)
	cfg.MaxConnLifetime = maxConnLifetime
	cfg.MaxConnLifetimeJitter = maxConnLifetime / 10
	cfg.PingTimeout = pingTimeout
	return cfg, nil
}
```

```go
	actx, cancel := context.WithTimeout(ctx, acquireWait)
	conn, err := pool.Acquire(actx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return ErrPoolExhausted
		}
		return fmt.Errorf("acquire database connection: %w", err)
	}
```

`newPoolConfig(dsn, budget, replicas)` keeps the DSN for connection details and the exec mode, but owns sizing, jitter and the ping timeout in code; the tests check those values with `ParseConfig` alone, and drive `withConn` with a dial that never completes to show it returns `ErrPoolExhausted` (mapped to 503) after 100 ms instead of hanging. No database, network or Docker is needed.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: MaxConns left at its default, max(4, runtime.NumCPU()). On a
	// 64-CPU node every pod opens 64 connections, and 20 pods exceed max_connections.
	// MISTAKE: no MaxConnLifetimeJitter; connections opened together at start
	// all expire together every hour and reconnect in a burst.
	cfg.MaxConnLifetimeJitter = 500 * time.Millisecond // MISTAKE: pgx rounds jitter down to whole seconds, so this is zero.
	// MISTAKE: no PingTimeout; a half-dead connection eats the whole request deadline
	// in the acquire-time ping instead of being dropped after a second.

	// MISTAKE: simple protocol "to make PgBouncer work". It interpolates arguments
	// client side (the path of the v5.9.2 SQL injection fix) and drops binary encoding;
	// PgBouncer 1.21+ supports the default prepared statements, and exec covers older ones.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
```

```go
	// MISTAKE: each pool.Exec may run on a different connection. The lock is taken on
	// one, the unlock runs on another and fails, and the lock leaks until that conn closes.
	// Behind a transaction pooler the server connection changes between statements too.
	if _, err := pool.Exec(ctx, "select pg_advisory_lock(42)"); err != nil {
		return err
	}
	defer pool.Exec(ctx, "select pg_advisory_unlock(42)")
```

The file also opens a second pool through `database/sql`, runs a handler query with `context.Background()`, and issues `LISTEN` through the pool. Every one of these works on a laptop with one replica and a direct connection, which is why they reach production: the failures need many pods, an hour of uptime, a full pool or a PgBouncer in front.

## Checklist

When reviewing code under this skill, confirm:

- [ ] The service uses `pgxpool` from pgx v5.9.2 or later (latest release preferred), and nothing opens a second pool through `database/sql` without a library that requires it.
- [ ] `MaxConns` is set in code from a documented budget and the maximum replica count including surge, not left at the `NumCPU` default or taken from `pool_max_conns` alone.
- [ ] The sum of `MaxConns` across all pods and other clients stays below `max_connections` minus reserved slots.
- [ ] `MaxConnLifetimeJitter` is at least one second and `PingTimeout` is greater than zero.
- [ ] Every acquire, explicit or through `pool.Query`/`Exec`, runs under a context with a deadline, never `context.Background()` in request code.
- [ ] Pool exhaustion is detected and mapped to 503, and `EmptyAcquireCount`/`EmptyAcquireWaitTime` are exported as metrics.
- [ ] No code sets `QueryExecModeSimpleProtocol` as the default; behind PgBouncer the mode is the default with `max_prepared_statements > 0`, or `exec`, never `describe_exec`.
- [ ] `LISTEN`, session advisory locks and session `SET` run on one held connection that bypasses transaction pooling.
- [ ] A test parses each deployed DSN shape with `ParseConfig`, so a pgx upgrade that changes parsing fails in CI.

## When not to apply

- **A single-connection CLI or migration job.** Use `pgx.Connect` with a deadline; a pool, jitter and 503 mapping add nothing to a process that runs one statement stream and exits.
- **A library that only accepts `*sql.DB`.** Some tools (migration runners, job queues with a `database/sql` driver) need it. Build it from the pgx pool with `stdlib.OpenDBFromPool` so there is still one pool and one budget, and keep your own queries on `pgxpool`.
- **One replica that owns the database.** With a single pod and no autoscaling, the budget is the whole of `max_connections` minus reserved slots; still set `MaxConns` explicitly, because the `NumCPU` default changes when the host does.
- **Background workers that must not drop work.** A job consumer should wait for a connection under its own longer deadline instead of returning 503; bound its concurrency to its share of `MaxConns` so it cannot starve request traffic.
- **A proxy or Postgres-compatible server without the extended protocol.** If it supports neither prepared statements nor `exec` mode, simple protocol is the only option: pin pgx at or above v5.9.2, never build SQL text from input, and document why the exception exists.
- **Managed poolers that pin sessions.** RDS Proxy and similar products have their own rules about which statements pin a client to a server connection; check their documentation before applying the PgBouncer guidance in rule 5 and rule 6.
