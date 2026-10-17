---
name: sql-queries-and-transactions
description: Use when writing or reviewing Go code that runs Postgres queries or transactions with pgx v5, choosing between sqlc, an ORM or hand-written Scan, building a query with a dynamic ORDER BY or filter, paginating a list, loading related rows for a page, retrying a transaction, or calling another service while a transaction is open. Covers sqlc with sql_package pgx/v5, $n parameters and allow-listed identifiers, keyset pagination instead of OFFSET, LEFT JOIN LATERAL with array_agg for small child sets and = ANY($1) or batches for large ones instead of N+1, a transaction helper that reruns the whole transaction on 40001/40P01 with jitter, the outbox instead of network calls inside a transaction, and per-role statement_timeout and idle_in_transaction_session_timeout.
metadata:
  verified: 2026-10-17
---

# SQL queries and transactions

## The decision

Once the pool exists (see `postgres-pgx-pool`), every feature adds queries and transactions, and the way they are written decides how the service behaves when the table holds ten million rows instead of ten. The failures do not show up in a demo: hand-written `Scan` code that breaks when a column is added, a sort parameter that becomes SQL injection, page 5,000 that takes seconds, a list endpoint that fires a query per row, a deadlock that surfaces as a 500 instead of being retried, and a transaction that holds row locks while it waits on a mail service. This skill fixes how queries are written and generated, how they take input, how lists are paged and loaded, and how transactions are run, retried and bounded. Pool sizing and exec modes belong to `postgres-pgx-pool`; schema changes belong to migrations.

## The rule

1. Write queries in `.sql` files and generate their Go code with sqlc (`sql_package: "pgx/v5"`), and for the rare query sqlc cannot express, scan with `pgx.CollectRows` and `pgx.RowToStructByName`, never with an ORM or a hand-written `Scan` loop.
2. Pass every value as a `$n` parameter, and when a column name or sort direction must vary, map the request's key through an allow-list to SQL text written in code, never `fmt.Sprintf` input into SQL.
3. Paginate with a keyset (`WHERE (created_at, id) < ($3, $4) ORDER BY created_at DESC, id DESC LIMIT $2`) and an opaque cursor, never with `OFFSET`.
4. Load related rows for a whole page without one query per row: when each parent has a small, bounded set of children, return them with the parents in the same statement through `LEFT JOIN LATERAL (SELECT array_agg(...) ...)` joined to a subquery that applies the page's `LIMIT`, and only when the child set is large or unbounded use a second query with `= ANY($1::bigint[])` or one `pgx.Batch` (sqlc `:batchmany`).
5. Run every transaction through one helper that takes `func(pgx.Tx) error` and reruns the whole function, with capped and jittered backoff, only on SQLSTATE `40001` or `40P01`.
6. Make no network call (HTTP, queue publish, email) inside a transaction: write the side effect to an outbox table, or enqueue with River's `InsertTx`, in the same transaction and deliver it after commit.
7. Set `statement_timeout` and `idle_in_transaction_session_timeout` (and `transaction_timeout` on PostgreSQL 17+) on the service's role with `ALTER ROLE ... SET`, as the server-side backstop for the request's context deadline.

## Why

**Rule 1.** A hand-written `Scan` lists columns in an order that only a comment ties to the query; add a column and `SELECT *` fails at run time with a field-count error, reorder two columns of the same type and it silently swaps values. sqlc parses the queries against the schema at generate time, so a renamed column fails `sqlc generate` in CI instead of a request in production, and the output is plain pgx code with no reflection. With `sql_package: "pgx/v5"` it generates against pgx directly (not `database/sql`), which is what makes `:batchmany` and `:copyfrom` available; set `emit_db_tags: true` so the same structs work with `RowToStructByName`. ORMs build SQL you never read, make N+1 the easy path through lazy relations, and add reflection on every row. When a query needs a dynamic `ORDER BY` or optional filters sqlc cannot express, write it by hand and collect it with `pgx.CollectRows(rows, pgx.RowToStructByName[T])`, which closes the rows and matches columns by name (case-insensitive, `db` tags override). Use `RowToStructByNameLax` only when the struct deliberately has more fields than the query returns.

**Rule 2.** With `$n` placeholders the value travels separately from the SQL text, so no value can change the statement; this is what pgx's default extended protocol gives you, and why `postgres-pgx-pool` keeps it. Placeholders work only for values, though: a column name, `ASC`/`DESC` or a table name cannot be a parameter, which is exactly where `Sprintf` creeps in. Map the request's key (`newest`, `oldest`) to a constant fragment and reject everything else; the allow-list doubles as documentation of which sorts have an index. `pgx.Identifier{...}.Sanitize()` quotes an identifier correctly, but quoting a column the user chose still lets them sort by any column, including unindexed and sensitive ones, so prefer the allow-list.

**Rule 3.** `OFFSET n` makes Postgres produce and discard `n` rows before the page, so cost grows linearly with depth and a crawler walking to the end turns every request into a near full scan. Rows inserted or deleted between two requests shift the window, so users see duplicates or miss rows. A keyset page starts where the last one ended: with an index on `(customer_id, created_at, id)` every page is an index range scan of `LIMIT` rows. Include a unique column (`id`) in the key, because `created_at` alone has ties and a tie at the page boundary drops rows. Encode the position as an opaque cursor so clients cannot depend on its shape and you can change it later; it is not a security boundary, so it still goes in as `$n` parameters. The cost: no "jump to page 37" and no exact page count. Most APIs and infinite-scroll UIs need neither.

**Rule 4.** Each query is a network round trip plus a pool checkout. A list of 100 customers that loads orders per customer is 101 round trips; at 1 ms each, that is 100 ms of pure latency before any query runs slowly, and it holds a connection the whole time. `WHERE customer_id = ANY($1::bigint[])` sends the IDs as one array parameter and returns everything in one round trip; group the rows in Go. sqlc generates a method that takes `[]int64` for this. When the queries differ (or each must return its own result set), queue them in a `pgx.Batch` and send it once with `SendBatch`, or annotate the query `:batchmany` and let sqlc generate the batch.

A second `= ANY` query is still a second round trip, a second set of protocol messages and a second wait on the connection for every request. When the children per parent are few and bounded (the items of an order, the tags of a post), return them in the same statement: `LEFT JOIN LATERAL (SELECT array_agg(sku ORDER BY sku), array_agg(qty ORDER BY sku) FROM order_items WHERE order_id = o.id)` gives each parent its children as parallel arrays that pgx decodes in binary, with no JSON and no second query, and `coalesce(..., '{}')` keeps parents with no children. Put the keyset `WHERE`, `ORDER BY` and `LIMIT` in a subquery and join the LATERAL to that subquery, so the child lookup runs once per row of the page (one index scan each) and not for parent rows the limit throws away. In the orders-api benchmark on one 2 vCPU / 1 GiB replica, replacing the `= ANY` items query with this shape was the change that moved CPU: 9% less CPU per request across three verification runs (16% less, and about 20% less Postgres CPU, in the run that measured it alone), and together with a correctly sized bulkhead (`overload-protection` rule 6) it doubled the capacity within the SLO from 5,000 to 10,000 req/s. Keep `= ANY` (or a batch) when the child set is large or unbounded, such as a customer's orders: one array per parent then grows without limit and must be paged on its own.

**Rule 5.** Under `REPEATABLE READ` and `SERIALIZABLE` Postgres aborts transactions with `40001 serialization_failure`, and at any isolation level it breaks deadlocks with `40P01 deadlock_detected`. After either error the transaction is aborted: every further statement fails with `25P02`, so retrying the one failed statement is useless. The Postgres docs say to retry the complete transaction, including the logic that decided what to write, which means the retry unit must be a function, not a statement. One helper built on `pgx.BeginTxFunc` gives every caller the same behavior: it commits on `nil`, rolls back on error, and reruns `fn` on `40001`/`40P01`, which also covers a serialization failure reported at `COMMIT`. Classify with `errors.AsType[*pgconn.PgError](err)` on `Code`, never by matching the message, and wrap inside `fn` with `%w` so the code survives. Retry nothing else: `23505 unique_violation` is usually a real conflict, and `57014 query_canceled` from `statement_timeout` repeats the same slow query. Cap attempts (five is plenty), add full jitter so the two colliding transactions do not collide again, and stop when the context ends. `BeginTxFunc`'s context bounds only `BEGIN`, `COMMIT` and `ROLLBACK`, so pass the request `ctx` to every query inside `fn` (see `context-and-deadlines`).

**Rule 6.** A transaction holds a connection, and after its first write, row locks, until it ends. An HTTP call inside it makes the database wait on someone else's latency: a mail service that takes 2 s holds the locks for 2 s, and when it hangs, other requests pile up behind those locks and exhaust the pool. It is also wrong on its own terms: if the transaction rolls back or is retried by rule 5, the email has already gone out, once per attempt. Publishing after commit instead loses the event when the pod dies in between. Writing the event as a row in the same transaction makes it atomic with the business change; a relay (or River's job workers, with `InsertTx`) delivers it after commit, at least once, so consumers must be idempotent. Reads from another service needed to decide what to write belong before `BEGIN`.

**Rule 7.** Context cancellation is a client-side mechanism: pgx sends a cancel request when the context ends, which needs the pod alive and the network working. A pod that is OOM-killed, frozen or partitioned leaves its query running and its transaction open, holding locks and blocking vacuum. `statement_timeout` aborts any statement that runs too long; `idle_in_transaction_session_timeout` terminates a session that sits idle inside an open transaction, which is exactly the pod that died between two statements; `transaction_timeout` (PostgreSQL 17+) caps the whole transaction. All default to 0, meaning disabled. The Postgres docs advise against setting `statement_timeout` in `postgresql.conf` because it affects every session, including migrations and maintenance, so set them per role: `ALTER ROLE app SET ...` applies to every new session of that role, also through PgBouncer in transaction mode, where a startup `options=-c` parameter may not reach the server. Set them above the request deadline, so in normal operation the context fires first and the server limit only catches what the client could not.

The cost: sqlc adds a code generation step to the build and makes dynamic queries a hand-written exception; keyset pages cannot jump; the outbox adds a table and a relay, and turns "exactly once" into "at least once with idempotent consumers". Each of those is cheaper than the incident it prevents.

## Good example

See [`examples/good/`](examples/good/).

```go
func InTx(ctx context.Context, db beginner, opts pgx.TxOptions, fn func(pgx.Tx) error) error {
	return retryTx(ctx, func() error {
		return pgx.BeginTxFunc(ctx, db, opts, fn)
	})
}
```

```go
func retryable(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (pgErr.Code == serializationFailure || pgErr.Code == deadlockDetected)
}
```

```go
	where := "customer_id = $1"
	if hasCursor {
		// The row comparison matches the index (customer_id, created_at, id),
		// so every page is an index range scan, however deep it is.
		where += " AND (created_at, id) " + s.cmp + " ($3, $4)"
	}
	return "SELECT id, customer_id, total_cents, created_at FROM orders WHERE " +
		where + " ORDER BY " + s.order + " LIMIT $2", nil
```

`InTx` reruns the whole function, and `retryTx` takes the attempt as a plain function, so the tests drive it with a fake that returns `*pgconn.PgError` codes, inside `synctest.Test` so the backoff is instant. The only concatenated text in `listOrdersSQL` comes from the `sorts` allow-list in code; values stay `$n`. `db.go` stands in for what sqlc generates from `query.sql` and `sqlc.yaml` (it is hand-written so the example builds without sqlc), `PlaceOrder` writes the order and its outbox row in one transaction, `OrdersFor` replaces N+1 with `= ANY` because a customer's orders are unbounded, and `roles.sql` sets the timeouts. Order items are few per order, so `OrderPageWithItems` in `query.sql` (and its stand-in in `items.go`) returns them with the page:

```sql
FROM (
    SELECT id, customer_id, total_cents, created_at FROM orders
    WHERE customer_id = $1
    ORDER BY created_at DESC, id DESC
    LIMIT $2
) o
LEFT JOIN LATERAL (
    SELECT array_agg(sku ORDER BY sku) AS skus, array_agg(qty ORDER BY sku) AS qtys
    FROM order_items WHERE order_id = o.id
) i ON true
```

The tests need no database: they check the SQL shape, including that the `LIMIT` sits inside the subquery before the `LEFT JOIN LATERAL` and that `items.go` matches `query.sql`.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: request text pasted into the SQL. sort=id;DROP TABLE orders-- or
	// customer=0 OR 1=1 runs as SQL; quoting by hand does not fix it.
	// MISTAKE: OFFSET reads and throws away every earlier row, so page 5000 scans
	// 100,000 rows, and an insert between requests shifts rows across pages.
	query := fmt.Sprintf("SELECT * FROM orders WHERE customer_id = %s ORDER BY %s LIMIT 20 OFFSET %d",
		customerID, sort, page*20)
```

```go
	if err := tx.QueryRow(ctx, q, customerID, cents).Scan(&id); err != nil {
		// MISTAKE: retrying one statement after an error. The transaction is already
		// aborted, so this fails with 25P02 and a deadlock (40P01) is never recovered.
		if strings.Contains(err.Error(), "deadlock") {
			err = tx.QueryRow(ctx, q, customerID, cents).Scan(&id)
		}
		if err != nil {
			return err
		}
	}
	// MISTAKE: an HTTP call inside the transaction. The row locks and the connection
	// are held for the mail service's latency, and a later rollback cannot unsend it.
	body := strings.NewReader(fmt.Sprintf(`{"order_id":%d}`, id))
	resp, err := http.Post("https://mail.internal/send", "application/json", body)
```

The file also scans `SELECT *` into a hand-written `Scan` and sums orders with one query per customer. Every one of these passes a demo with ten rows and one user, which is why they ship: the failures need a deep page, a hostile parameter, two concurrent writers or a slow mail service.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Static queries live in `.sql` files generated by sqlc with `sql_package: "pgx/v5"`, the generated code is committed and unedited, and no ORM or hand-written `Scan` loop is added.
- [ ] Every value reaches Postgres as a `$n` parameter, and every dynamic identifier or sort direction comes from an allow-list of constants in code.
- [ ] No list endpoint uses `OFFSET`; pages use a keyset on an indexed key that ends in a unique column, behind an opaque cursor.
- [ ] No loop runs a query per item; small, bounded child rows come back with their parents in one statement (`LEFT JOIN LATERAL` with `array_agg`, the `LIMIT` in a subquery before the join), and only large or unbounded ones load with a second `= ANY($1::type[])` query or one `pgx.Batch`/`:batchmany`.
- [ ] Every transaction goes through the one helper, which reruns the whole function only on `40001`/`40P01`, with capped attempts, jitter and a stop on context end.
- [ ] Errors are classified with `errors.AsType[*pgconn.PgError]` on `Code`, never by message text, and code inside a transaction wraps with `%w`.
- [ ] No HTTP call, queue publish or email happens between `BEGIN` and `COMMIT`; side effects are outbox rows or `InsertTx` jobs in the same transaction.
- [ ] Every query inside a transaction function is passed the caller's `ctx`, because `BeginTxFunc` bounds only `BEGIN`, `COMMIT` and `ROLLBACK`.
- [ ] The service's role has `statement_timeout` and `idle_in_transaction_session_timeout` set (and `transaction_timeout` on PostgreSQL 17+), above the request deadline.

## When not to apply

- **Truly dynamic queries.** Report builders and admin search with many optional filters do not fit sqlc. Build them by hand in one small function: placeholders for every value, an allow-list for every identifier, `CollectRows` for the result, and a test of the generated SQL. A query builder is acceptable there if it only emits placeholders; keep it out of the static queries.
- **Bulk loads.** Inserting thousands of rows is neither N+1 nor a batch problem: use `CopyFrom` (sqlc `:copyfrom`), and run it outside request paths with its own deadline.
- **Small, bounded lists.** A settings page with at most 50 rows needs no pagination at all, and an admin table that truly needs page numbers over a few thousand rows can use `OFFSET`; switch to a keyset before the table or the depth grows.
- **Single-statement writes.** An `INSERT ... ON CONFLICT` or one `UPDATE` is atomic on its own; it does not need `InTx`. A deadlock can still hit it, so retry it with the same classification when it touches rows other writers lock in a different order.
- **Effects that must happen before commit.** Taking a payment authorization or reserving a resource in another system cannot be an outbox row. Do it before `BEGIN`, store its ID in the transaction, and compensate (void, release) when the transaction fails.
- **Long jobs and migrations.** Batch jobs, backfills and migrations run as their own role with longer or disabled timeouts, never under the request role's `statement_timeout`; migrations set `lock_timeout` as their own decision.
- **Non-Postgres engines.** The SQLSTATE codes and the timeout settings in this skill are Postgres's. On another engine (SQLite has no SQLSTATE codes at all), keep the shape of the helper, but classify retryable errors from that engine's documentation.
