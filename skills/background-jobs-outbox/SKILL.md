---
name: background-jobs-outbox
description: Use when writing or reviewing Go code that runs work outside the request on Postgres, enqueues a job or event after a business write, builds a worker, relay or job table, uses LISTEN/NOTIFY, picks a job queue library, or decides how jobs retry, fail and shut down. Covers enqueueing in the same transaction with River's InsertTx or an outbox row, River as the default and when to hand-roll an outbox relay, claiming with FOR UPDATE SKIP LOCKED and a lease, LISTEN/NOTIFY only as a wake-up, idempotent handlers under at-least-once delivery, worker concurrency bounded by the pool, retries with backoff and a dead state, graceful worker shutdown, River migrations before workers, and River Pro paid features.
metadata:
  verified: 2026-10-17
---

# Background jobs and the outbox

## The decision

Some work must happen because a row was written, but not inside the request that wrote it: send the receipt, call the payment provider's webhook, publish `order.placed` to the broker, resize the upload. `sql-queries-and-transactions` already forbids doing that work inside the transaction and says to write it down instead. This skill covers the other half: where that written-down work lives, which process picks it up, how several replicas share it without doing it twice or not at all, and what happens when it fails, when a pod dies mid-job and when the deploy sends SIGTERM. Get it wrong and the failure is silent: receipts that never go out because a notification had no listener, receipts for orders that were rolled back, two replicas charging the same card, or 10,000 goroutines queueing on a 20-connection pool after an outage. Pool sizing belongs to `postgres-pgx-pool` and goroutine ownership to `concurrency`; this skill applies both to workers.

## The rule

1. Enqueue background work in the same transaction as the business write, with River's `InsertTx` or an `INSERT` into an outbox table, never on the pool before `COMMIT` or after it.
2. Default to River (`github.com/riverqueue/river` with `riverpgxv5`) for jobs this service runs, applying its migrations in the deploy's migration step before any worker starts, and hand-roll an outbox table only to relay events to a broker or another service.
3. Claim work with `FOR UPDATE SKIP LOCKED` and a `LIMIT` in one short statement that leases the rows, and never keep a transaction open while the job calls the network.
4. Use LISTEN/NOTIFY only to wake workers early over a durable table, with an empty payload, a dedicated connection and a polling fallback, never as the queue.
5. Make every handler idempotent on a business key (a record of work done plus a stable idempotency key sent to the external API), because delivery is at least once.
6. Bound each process's worker concurrency to a fraction of the pgx pool's `MaxConns`, never one goroutine per job.
7. Give every job a per-attempt timeout, a capped number of attempts with jittered backoff, and a terminal state (`discarded`/`cancelled` in River, `dead_at` in an outbox) that someone is alerted on.
8. Stop workers gracefully on SIGTERM: stop fetching, let running jobs finish within a timeout shorter than the platform's grace period, then cancel their contexts.

## Why

**Rule 1.** There are two ways to get the order wrong. Enqueue after `COMMIT` and a crash, a deploy or a failed insert between the two leaves the order without its receipt, with nothing to retry. Enqueue on the pool before `COMMIT` and a rollback, or a rule-5 retry from `sql-queries-and-transactions`, leaves a job for an order that does not exist, and the worker may even pick it up before the order is visible. In the same transaction, the job is part of the write: River's `InsertTx` docs state that the job is not worked until the transaction commits and disappears if it rolls back. An outbox row gets the same guarantee from plain Postgres. `client.Insert` (no `Tx`) is for jobs not tied to a write, and it needs a client built with a pool.

**Rule 2.** A job table with claiming, retries, backoff, scheduling, uniqueness, stuck-job rescue, cleanup of finished rows, leader election and a web UI is a project, and River already is one, built on pgx and on the same Postgres you have. It claims jobs with `FOR UPDATE SKIP LOCKED` (the `JobGetAvailable` query in `riverpgxv5`), uses LISTEN/NOTIFY to fetch quickly and polls every `FetchPollInterval` (default 1 s) as the fallback, and moves a job to `discarded` after `MaxAttempts`. The examples use River v0.49.0 (October 2026), which declares `go 1.26.0`, the repository floor; it is pre-1.0, so read the changelog on every upgrade, which is where new migrations are announced. River's docs say to migrate before starting workers: run `river migrate-up --line main` (or `rivermigrate` from your migrate command, or export the SQL with `river migrate-get` into your goose files, as `db-migrations` wants) as the same pre-rollout step as your schema, and have workers call `Validate` and refuse to start on an old schema, as the good example does. The hand-rolled outbox is for the case River does not cover: the consumer is not a Go worker in this service but Kafka, NATS, SNS or another team's webhook. **River Pro is paid** (a commercial license, priced per month): workflows, sequences, batching, global concurrency limits, a dead letter queue, durable periodic jobs, encrypted jobs, ephemeral jobs and per-queue retention. Mark any use of those in the code review and the design doc as a paid dependency; everything in this skill uses open-source River.

**Rule 3.** A worker that runs `SELECT ... WHERE done = false LIMIT 100` with no lock reads the same rows as every other replica, so each job runs once per replica. Adding `FOR UPDATE` without `SKIP LOCKED` makes the second worker wait for the first one's rows, so the replicas take turns instead of working in parallel. The Postgres docs describe `SKIP LOCKED` exactly for this: it gives "an inconsistent view of the data, so this is not suitable for general purpose work, but can be used to avoid lock contention with multiple consumers accessing a queue-like table". Holding that row lock while publishing to the broker is the same mistake as an HTTP call inside a transaction: the connection and locks wait on the broker's latency. So claim in one autocommit `UPDATE ... WHERE id IN (SELECT ... FOR UPDATE SKIP LOCKED)` that moves `available_at` forward as a lease, publish with no transaction open, then mark the row sent. A relay that dies mid-batch loses nothing; its rows reappear when the lease ends. River does the equivalent with its `running` state and the job rescuer. Index only the pending rows (a partial index), or the claim slows down as sent rows pile up.

**Rule 4.** `NOTIFY` is delivered only to sessions that have already run `LISTEN` on the channel; nobody listening during a deploy, a crash or a network blip means the message is gone, and nothing records that it was lost. It is also delivered only if the sending transaction commits (which is what makes it a safe hint), payloads must be under 8000 bytes by default, identical notifications in one transaction are folded into one, and if the notification queue (8 GB in a standard install) fills because a listener stopped reading, every transaction that calls `NOTIFY` fails at commit. So the table is the queue and the notification only says "look now": send an empty payload, drain on every wake-up and on a ticker. `LISTEN` is session state, so it needs one held connection that reaches Postgres directly or through session pooling (`postgres-pgx-pool` rule 6), and that connection must never go back to the pool; River's listener does this and `PollOnly` turns it off behind a transaction-mode PgBouncer.

**Rule 5.** No queue gives exactly-once execution. River reruns a job whose worker crashed after `RescueStuckJobsAfter` (default 1 hour), a lease expires while a slow publish is still running, and the worker can die after the email left and before it recorded success. Idempotency is the only defense: check a done-record keyed by the business ID (`receipts.order_id` as primary key, `ON CONFLICT DO NOTHING` when marking), and pass an idempotency key derived from that ID, not from the attempt, to the external API, so the gap between "sent" and "recorded" is deduplicated by the provider. Outbox consumers get the same at-least-once delivery and dedupe on the event ID. Keep job arguments to IDs and read current data in the worker; a 2 MB payload in every row bloats the table and goes stale.

**Rule 6.** A job usually needs a connection, and the pool is shared with the request path. `MaxWorkers` is per queue and per client, so the real total is the sum over queues times the number of replicas; keep the per-process sum well below `MaxConns` (the example gives workers half) and size the server's `max_connections` for all replicas, plus one dedicated connection per replica for the listener. A goroutine per claimed row turns a backlog after an outage into a pool-exhaustion incident and has no owner at shutdown (`concurrency` rules 2 and 4). River's `MaxWorkers` is that bound; a hand-written relay processes its batch in its own loop or under `errgroup.SetLimit`.

**Rule 7.** Without a timeout a hung API holds a worker slot forever; River's `JobTimeout` defaults to 1 minute, and `Timeout` on the worker overrides it per kind. Without backoff a broker outage becomes a tight retry loop; River's default schedule waits attempt⁴ seconds with ±10% jitter (1 s, 16 s, 81 s, ...). Without a cap a poison job retries forever: River defaults to 25 attempts, about three weeks of retries, so set `MaxAttempts` per kind to what the work can tolerate. Return a plain error for transient failures, `river.JobCancel(err)` for permanent ones (it ends the job without further attempts), and `river.JobSnooze(d)` to wait without spending an attempt. Discarded, cancelled and dead rows are where lost work lives: alert on their count, and give someone a way to fix and requeue them.

**Rule 8.** On a deploy, Kubernetes sends SIGTERM and kills the pod after `terminationGracePeriodSeconds` (30 s by default). A worker that exits immediately abandons running jobs until the rescuer finds them; one that waits indefinitely is killed mid-send anyway. Stop fetching first, give running jobs a bounded time, then cancel their contexts so they return. River v0.38.0+ does this with `SoftStopTimeout`: cancelling the context passed to `Start` begins a soft stop that escalates to cancelling jobs after the timeout, and `<-client.Stopped()` waits for it; `Stop` and `StopAndCancel` are the manual equivalents. Workers must honor `ctx` for any of this to work; returning `river.JobSnooze(0)` on shutdown hands the job back without counting an attempt.

The cost: a job table and a relay are more moving parts than `go sendEmail()`, at-least-once delivery forces every handler to be idempotent, and River is a pre-1.0 dependency with its own migrations. Each of those is cheaper than an order without its receipt or a customer charged twice.

## Good example

See [`examples/good/`](examples/good/).

```go
	err := pgx.BeginTxFunc(ctx, o.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, insertOrderSQL, customerID, totalCents).Scan(&id); err != nil {
			return fmt.Errorf("insert order: %w", err)
		}
		if _, err := o.river.InsertTx(ctx, tx, SendReceiptArgs{OrderID: id}, nil); err != nil {
			return fmt.Errorf("enqueue receipt: %w", err)
		}
		payload := fmt.Sprintf(`{"order_id":%d}`, id)
		if _, err := tx.Exec(ctx, insertOutboxSQL, "order.placed", payload); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
```

The order, its River job and its outbox event commit together or not at all, and nothing in the transaction touches the network.

```go
	claimSQL = `
UPDATE outbox SET attempts = attempts + 1,
       available_at = now() + $2::float8 * interval '1 second'
WHERE id IN (
    SELECT id FROM outbox
    WHERE sent_at IS NULL AND dead_at IS NULL AND available_at <= now()
    ORDER BY available_at, id
    LIMIT $1
    FOR UPDATE SKIP LOCKED)
RETURNING id, topic, payload, attempts`
```

```go
	sent, err := w.store.ReceiptSent(ctx, id)
	if err != nil {
		return fmt.Errorf("check receipt %d: %w", id, err)
	}
	if sent {
		return nil // An earlier attempt finished the work.
	}
	// The key is derived from the order, not the attempt, so a retry after a
	// crash between Send and MarkReceiptSent is deduplicated by the provider.
	key := fmt.Sprintf("receipt-%d", id)
	if err := w.mailer.Send(ctx, id, key); err != nil {
		if errors.Is(err, ErrInvalidAddress) {
			return river.JobCancel(fmt.Errorf("send receipt %d: %w", id, err))
		}
		return fmt.Errorf("send receipt %d: %w", id, err) // retried with backoff
	}
```

```go
	return &river.Config{
		Queues: map[string]river.QueueConfig{
			queueReceipts: {MaxWorkers: int(maxConns) / 2},
		},
		Workers:         workers,
		SoftStopTimeout: shutdownGrace - 10*time.Second,
	}
```

The relay claims a leased batch and publishes with no transaction open, `afterFailure` moves an event to the dead state after `maxAttempts` with capped, jittered backoff before that, and `runRelay` drains on every wake-up and on a ticker, so a lost notification only delays an event. `main` validates River's migrations instead of running them, sizes workers from the pool and soft-stops within the grace period. The tests need no database: a fake store and mailer show that a retried job delivers one receipt, `synctest` drives the relay loop through polls, a wake-up and shutdown, and the claim SQL and config are checked as values.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: enqueued on the pool, outside tx, before commit. If the commit
	// fails the job still exists: a receipt for an order that was never placed.
	if _, err := pool.Exec(ctx, "INSERT INTO jobs (order_id) VALUES ($1)", id); err != nil {
		return err
	}
	// MISTAKE: NOTIFY as the queue. With no listener connected (deploy, crash,
	// PgBouncer in transaction mode) this message is gone for good.
	if _, err := tx.Exec(ctx, "SELECT pg_notify('receipts', $1)", strconv.FormatInt(id, 10)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// MISTAKE: enqueued after commit with Insert instead of InsertTx. A crash
	// or a failed insert here loses the job while the order exists.
	_, err = client.Insert(ctx, SendReceiptArgs{OrderID: id}, nil)
```

```go
	for i := range ids {
		// MISTAKE: one goroutine per job with no limit and no owner. A backlog of
		// 10,000 rows opens 10,000 queries against a 20-connection pool, and
		// shutdown kills them mid-send.
		go func() {
			// MISTAKE: not idempotent. A crash between the send and the UPDATE,
			// or a second replica, sends the receipt again.
			sendReceipt(ctx, orders[i])
			pool.Exec(ctx, "UPDATE jobs SET done = true WHERE id = $1", ids[i])
		}()
	}
```

The file also polls the job table with a plain `SELECT ... LIMIT 100` and no `SKIP LOCKED`, so every replica sends the same receipts. Each of these works with one replica, no deploys and a mail API that never fails, which is why they ship: the failures need a crash at the wrong moment, a second pod or a backlog.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Every job or event caused by a write is inserted with `InsertTx` or an outbox `INSERT` on the same `tx`, and nothing is enqueued on the pool before `COMMIT` or after it.
- [ ] Jobs this service runs use River; a hand-rolled job table exists only to relay events to another system, and any River Pro feature is flagged as a paid dependency.
- [ ] River's migrations run in the deploy's migration step before workers roll out, and workers do not migrate on startup.
- [ ] Every claim of pending rows uses `FOR UPDATE SKIP LOCKED` with a `LIMIT` on a partial index, and no transaction stays open across a network call.
- [ ] LISTEN/NOTIFY carries no data, runs on one held connection that is never returned to the pool, and a polling interval drains the table when no notification arrives.
- [ ] Each handler checks a done-record keyed by the business ID and sends a stable idempotency key to external APIs, so running it twice has the effect of running it once.
- [ ] The sum of `MaxWorkers` (or relay concurrency) per process is below the pool's `MaxConns`, with room left for requests, and no code starts a goroutine per job.
- [ ] Every job kind has a timeout, a deliberate `MaxAttempts`, backoff with jitter, `JobCancel` for permanent errors, and an alert on discarded, cancelled or dead rows.
- [ ] SIGTERM stops fetching, lets running jobs finish within a timeout below the grace period, then cancels them, and handlers return when `ctx` ends.

## When not to apply

- **Best-effort work that may be lost.** Warming a cache or recording a non-billing metric after a response can go to an owned in-process worker with a bounded channel (`concurrency` rule 5); a crash losing a few is acceptable and a table is overhead.
- **Effects that must succeed before the write.** Authorizing a payment or reserving stock in another system cannot wait for a worker; do it before `BEGIN` and compensate on failure (`sql-queries-and-transactions`, "When not to apply").
- **An existing broker that is the system of record.** When the event is born in Kafka or NATS (no Postgres write causes it), consume from there with the broker's own acknowledgement and offsets; an outbox only exists to make a database write and a publish atomic.
- **Very high job volume.** A Postgres queue costs writes, WAL and vacuum on every job; at many thousands of jobs per second, or with very large payloads, measure table bloat and autovacuum lag, and move the hot path to a broker while keeping the outbox for the transactional boundary.
- **No Postgres.** River's SQLite driver is documented as experimental; on `sqlite-single-node` a small in-database table drained by one process is enough, since there are no competing replicas to skip locks for.
- **Transaction-mode PgBouncer only.** If workers cannot reach Postgres directly or through session pooling, set River's `PollOnly` and drop LISTEN entirely; latency becomes the poll interval, nothing else changes.
- **One-off scripts and backfills.** A batch job run by hand can loop over keyset pages in the foreground with its own role and timeouts; it needs idempotency, not a queue.
