---
name: sqlite-single-node
description: Use when deciding whether a Go service can use SQLite as its production database, opening a SQLite file from Go, choosing a SQLite driver, writing SQLite transactions, seeing "database is locked" or SQLITE_BUSY, or setting up SQLite backups and restore. Covers when one node owning the data makes SQLite viable and when to move to Postgres, modernc.org/sqlite as the cgo-free default (ncruces or mattn only with a reason, never crawshaw), WAL, busy_timeout, synchronous and foreign_keys set per connection through the DSN, one writer connection plus a separate read pool, BEGIN IMMEDIATE through _txlock, and continuous backup with Litestream 0.5.2 or later instead of LiteFS or file copies.
---

# SQLite single node

## The decision

A service that runs on one node can keep its data in a SQLite file next to the binary: no database server, no network hop, reads in microseconds, one static binary and one volume to operate. The decision is whether the workload fits that shape and, if it does, how to open the file so it survives concurrent requests. The defaults do not: SQLite starts in rollback-journal mode with `busy_timeout` at 0 and foreign keys off, `database/sql` hands out many connections that can all try to write, and a plain `BEGIN` defers the write lock until it is too late to wait for it. The result is "database is locked" under the first real load, silently accepted orphan rows, and a backup that is a copy of a file missing its WAL. This skill fixes when SQLite is the right database, which driver to use, how to open it, how to start write transactions and how to back it up. For Postgres, see `postgres-pgx-pool`, `sql-queries-and-transactions` and `db-migrations`.

## The rule

1. Use SQLite as the production database only when one service process on one node owns the file on local disk, and move to Postgres as soon as a second node must write to the data or take over without a restore.
2. Use `modernc.org/sqlite` through `database/sql` by default, choose `github.com/ncruces/go-sqlite3` or `github.com/mattn/go-sqlite3` only for a documented reason, and never use `crawshaw.io/sqlite`.
3. Set `busy_timeout(5000)`, `journal_mode(WAL)`, `synchronous(NORMAL)` and `foreign_keys(ON)` as `_pragma` parameters in a DSN built in code, so every connection the pool opens runs them, never with a one-off `db.Exec("PRAGMA ...")`.
4. Open the file as two `*sql.DB`: a writer with `SetMaxOpenConns(1)` that every write goes through, and a read pool opened with `_query_only=1` and sized to `max(4, runtime.GOMAXPROCS(0))`.
5. Start every write transaction with `BEGIN IMMEDIATE` by setting `_txlock=immediate` on the writer DSN, so a transaction that reads before it writes never has to upgrade its lock.
6. Replicate the database continuously with Litestream v0.5.2 or later to object storage and rehearse the restore, never LiteFS and never a copy of the live file.

## Why

**Rule 1.** SQLite allows one writer at a time per file, and WAL mode needs every process that opens the file to share memory on the same host; the SQLite docs state that WAL does not work over a network filesystem. That makes the boundary concrete. One node, one process (plus short-lived tools like a migration job or an admin CLI on the same host), a local or block-storage volume: SQLite fits, and it is cheaper and faster than a database server for most small and mid-sized services. Two pods writing, a shared NFS or EFS volume, active-active regions, or failover that must not lose a commit: SQLite does not fit, and no driver setting changes that. Replication with Litestream is asynchronous, so losing the node means restoring from object storage on a new one, with minutes of downtime and the last moments of commits possibly gone. If that recovery is not acceptable, or the single writer connection becomes the bottleneck (writers queue in `database/sql` waiting for it), or you need a second writing node, move to Postgres. On Kubernetes this also means one replica and a `Recreate` rollout: a rolling update starts a second pod that either cannot mount the volume or becomes a second writer and a second Litestream replicating the same database.

**Rule 2.** `modernc.org/sqlite` is SQLite transpiled to Go, so it needs no cgo: `CGO_ENABLED=0`, `distroless/static`, `scratch` and cross-compiling to arm64 keep working (see `container-runtime-tuning` and `ci-guardrails` for what cgo costs). Litestream 0.5 itself switched to it when it removed cgo. v1.60.1 is current, bundles SQLite 3.53.4 and requires Go 1.26; take the latest release, because each one tracks a newer SQLite. `github.com/ncruces/go-sqlite3` is also cgo-free (a Wasm build of SQLite translated to Go with wasm2go) and exposes much of the C API that `database/sql` hides: online backup, incremental BLOB I/O, custom VFSes, encryption at rest; choose it when you need one of those. `github.com/mattn/go-sqlite3` links the C library through cgo; choose it only when cgo is already in your build and a benchmark of your own queries shows a gain, because the published benchmarks ([go-sqlite-bench](https://github.com/cvilsmeier/go-sqlite-bench)) show no overall winner. `crawshaw.io/sqlite` has not released since v0.3.2 in June 2020, so it carries none of SQLite's fixes since. The drivers register different names (`sqlite` for modernc, `sqlite3` for the other two) and do not share DSN keys, so a switch means rewriting the DSN and rerunning the pragma tests.

**Rule 3.** Every PRAGMA except `journal_mode=WAL` belongs to the connection that ran it, not to the file. `database/sql` opens connections lazily, closes idle ones beyond `SetMaxIdleConns` (2 by default) and reopens them later, so a `db.Exec("PRAGMA busy_timeout=5000")` at startup configures whichever connection it happened to run on and nothing that opens afterwards. On every other connection `busy_timeout` is 0, so any lock contention fails at once with "database is locked", and `foreign_keys` is off, which is SQLite's default, so orphan rows are accepted. modernc runs each `_pragma` value on every new connection before the pool hands it out, in the order they appear; put `busy_timeout` first so the switch to WAL waits for a lock instead of failing. The values: WAL lets readers and the writer run at the same time; 5000 ms is what Litestream recommends and covers ordinary write bursts; `synchronous=NORMAL` in WAL mode skips the fsync on each commit and syncs at checkpoints, so the database stays consistent, a process crash loses nothing, and a power loss or OS crash can roll back the last commits, which Litestream's asynchronous replication already allows. `_pragma` values run as SQL text with `PRAGMA` prepended, so the DSN must be built in code; for a DSN that comes from configuration, call `sqlite.StrictPragmas(true)`. For plain file names, modernc silently drops query keys it does not recognize, so only a test that queries the PRAGMAs proves they apply.

**Rule 4.** SQLite serializes writers anyway; the question is where they wait. With a pool of 32 writable connections, they wait inside SQLite, in a busy handler that sleeps and retries, ignores your context, and fails transactions that tried to upgrade (rule 5). With one writer connection, they wait in `database/sql` for that connection, in order, under the request's context, which is where a deadline can turn saturation into a 503 (see `overload-protection`). Readers do not need the lock in WAL mode, so they get their own pool sized to the CPUs the process may use (`GOMAXPROCS` is container-aware since Go 1.25); more connections than that add nothing to in-process, CPU-bound reads. Set `SetMaxIdleConns` to the same value so connections are not closed and reopened. `_query_only=1` makes a write sent to the read pool fail with an error instead of quietly becoming a second writer. The cost of one writer is that a long write transaction blocks every other write: keep write transactions short, never make a network call inside one (see `sql-queries-and-transactions`), and use only the `*sql.Tx` inside the transaction function, because a call to `db.W` there waits for the connection the transaction already holds and deadlocks. Close every `Rows` and bound reads with the request context, because a read transaction left open stops checkpoints from resetting the WAL, which then grows without bound.

**Rule 5.** A plain `BEGIN` is `DEFERRED`: the first `SELECT` starts a read transaction, and the first write tries to upgrade it. The SQLite docs say that if another connection has already modified the database, the upgrade is not possible and the write fails with `SQLITE_BUSY`, and SQLite skips the busy handler when waiting could deadlock, so `busy_timeout` does not help. In WAL mode this surfaces as `SQLITE_BUSY_SNAPSHOT` (517). `BEGIN IMMEDIATE` takes the write lock before the first statement; if another connection holds it, `BEGIN` itself waits under `busy_timeout`, and once it succeeds nothing in the transaction can fail for lock reasons. With one writer connection in one process the race needs a second connection, but production always has one sooner or later: a migration job, an admin CLI, the `sqlite3` shell, the old pod during a deploy. In the good example's test, switching the writer DSN to `_txlock=deferred` makes 71 of 160 concurrent read-then-write transactions from two writers fail; with `immediate` all 160 commit. modernc sends `BEGIN IMMEDIATE` for every `BeginTx` on that `*sql.DB` except those with `ReadOnly: true`. If `BEGIN IMMEDIATE` still returns `SQLITE_BUSY` after five seconds, the writer is saturated: return the error (classify it with `errors.AsType[*sqlite.Error]` and `Code()&0xff == sqlite3.SQLITE_BUSY`, never by message text) and shed load rather than retrying in a loop.

**Rule 6.** The database is one file on one disk, so the backup is the high-availability story. Litestream runs as a separate process, follows the WAL and uploads every committed change to S3-compatible storage, which turns "the disk died" into "restore on a new node and start". v0.5 replaced the old WAL-segment format with LTX files that support point-in-time restore, takes one `replica` per database instead of a `replicas` list, and cannot restore v0.3 backups. v0.5.0 shipped restore bugs that v0.5.1 and v0.5.2 fixed, so v0.5.2 is the floor; v0.5.17 is current. Only one process may replicate into a given bucket path, or restores break. A backup you have never restored is a hope: run `litestream restore` against a scratch path on a schedule and open the result. LiteFS is the wrong tool even though it comes from the same team: Fly.io shifted its focus back to Litestream, sunset LiteFS Cloud, and LiteFS needs a FUSE mount and a lease service for what is still a single-writer database. Copying the live file is not a backup: committed transactions may still be in `app.db-wal`, and a copy taken during a checkpoint can be torn.

The cost: one node is a single point of failure with a restore-time recovery, write throughput is bounded by one connection, and the schema and SQL dialect differ from Postgres, so a later move is a migration project, not a configuration change. For services that fit, that buys no database server to run, pay for or reach over the network.

## Good example

See [`examples/good/`](examples/good/).

```go
var pragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(ON)",
}
```

```go
	w, err := sql.Open("sqlite", dsn(path, url.Values{"_txlock": {"immediate"}}))
	if err != nil {
		return nil, fmt.Errorf("open writer %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("ping writer %s: %w", path, err)
	}

	r, err := sql.Open("sqlite", dsn(path, url.Values{"_query_only": {"1"}}))
```

`Open` builds both DSNs in code from one pragma list, opens the writer first so the file is in WAL mode before readers connect, and `Write` runs every write through `BeginTx` on the writer, which sends `BEGIN IMMEDIATE`. The tests use a real file in `t.TempDir()`: they query the PRAGMAs on four held reader connections and the writer, check that a second writer connection cannot be acquired, that foreign keys reject an orphan, that two writer pools on one file (two processes) commit 160 concurrent increments without `SQLITE_BUSY`, that a deferred transaction fails its upgrade at once despite `busy_timeout`, and that a reader is not blocked by an open write transaction. `litestream.yml` is the backup configuration that runs next to it.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: one pool for reads and writes, with many connections. Every one of
	// them may write, so concurrent writers fight over the single write lock.
	db.SetMaxOpenConns(32)
	// MISTAKE: PRAGMAs sent once through the pool run on one connection. WAL is
	// stored in the file, but busy_timeout, synchronous and foreign_keys are per
	// connection: every other connection has busy_timeout=0, fails at once with
	// "database is locked", and accepts rows that violate foreign keys.
	for _, p := range []string{"journal_mode=WAL", "busy_timeout=5000", "synchronous=NORMAL", "foreign_keys=ON"} {
		if _, err := db.Exec("PRAGMA " + p); err != nil {
			return nil, err
		}
	}
```

```go
	// MISTAKE: a plain BeginTx sends a deferred BEGIN. The SELECT takes a read
	// snapshot; if another connection commits first, the UPDATE cannot upgrade
	// and fails with SQLITE_BUSY immediately, without consulting busy_timeout.
	tx, err := db.BeginTx(ctx, nil)
```

The file also describes, at the import, picking a cgo or unmaintained driver without a reason, and backs up by copying the live file. All of it passes a demo with one request at a time, because the first connection got the PRAGMAs and nobody else was writing.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Exactly one process on one node writes the file, it lives on local or block storage (not NFS, EFS or SMB), and the deployment cannot run two writers at once (one replica, `Recreate` rollout).
- [ ] The driver is `modernc.org/sqlite` at a current release, or `ncruces`/`mattn` with the reason written down; `crawshaw.io/sqlite` is absent.
- [ ] `busy_timeout`, `journal_mode=WAL`, `synchronous=NORMAL` and `foreign_keys=ON` are in a DSN built in code, and no code configures them with a one-off `Exec("PRAGMA ...")`.
- [ ] A test queries the PRAGMAs on several held connections of each pool against a real file, so an ignored or misspelled DSN key fails in CI.
- [ ] All writes go through one `*sql.DB` with `SetMaxOpenConns(1)`, and reads use a separate pool opened with `_query_only=1`.
- [ ] The writer DSN sets `_txlock=immediate` (or the driver's equivalent), and no write path uses a deferred transaction that reads before it writes.
- [ ] Code inside a write transaction uses only its `*sql.Tx`, makes no network call, and stays short; `SQLITE_BUSY` is classified by code, not by message, and shed rather than retried in a loop.
- [ ] Litestream v0.5.2 or later replicates the database to object storage, only one process replicates to that path, and a scheduled restore test opens the result.
- [ ] There is a written trigger for moving to Postgres: a second writing node, failover without a restore, or the writer connection saturating.

## When not to apply

- **Several nodes need to write, or failover must be instant.** Use Postgres with `postgres-pgx-pool`; distributed SQLite products (LiteFS, libSQL/Turso, rqlite) are separate systems with their own consistency models and operations, not settings on this one.
- **Tests for Postgres code.** This skill is not a license to stand SQLite in for Postgres in tests; the dialects and locking differ (see `testing`). Use a real SQLite file only when SQLite is the production database.
- **Embedded, read-mostly or CLI use.** A CLI, a desktop app or a cache that one goroutine uses can open one `*sql.DB` with the same pragmas and `SetMaxOpenConns(1)`; the separate read pool and Litestream add nothing when the data can be rebuilt or lives with the user.
- **A read-only dataset shipped with the binary.** Open it with `mode=ro` (or `immutable=1`) in a `file:` URI and skip the writer, WAL and Litestream entirely.
- **A one-off snapshot.** For a copy to hand to someone or seed an environment, use `VACUUM INTO 'snapshot.db'` or SQLite's online backup API (both modernc and ncruces expose it on the raw connection), never a file copy; keep Litestream for continuous protection.
- **Very high write rates.** Litestream's tips suggest `PRAGMA wal_autocheckpoint = 0` under tens of thousands of small write transactions per second, leaving checkpoints to Litestream; measure first, because without Litestream running that setting lets the WAL grow without bound.
- **A durable-on-commit requirement.** If every acknowledged commit must survive a power loss on that node, use `synchronous(FULL)` and accept an fsync per commit; Litestream still loses the latest commits if the disk itself is gone, so that requirement usually means Postgres with synchronous replication.
