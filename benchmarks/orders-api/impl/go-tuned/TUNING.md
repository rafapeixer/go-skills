# Tuning notes for go-tuned

`go-tuned` started as a copy of `impl/go-with` plus the pprof listener in
`debug.go`. Every change below was measured with the harness in
`benchmarks/orders-api/harness` on the same host and the same day, one
benchmark at a time, with the stack from `compose.yaml` (one app replica with
2 vCPU and 1 GiB, Postgres with 2 vCPU, 1 GiB and `max_connections=80`). The
rules of the comparison did not change: the contract tests pass unchanged in
every run, the pool stays at 8 connections per replica (80 / 10 replicas
including surge; peak `pg_stat_activity` count in every run was 9, the 8 app
connections plus the harness sampler), nothing is cached across requests,
and the dependencies are the standard library plus pgx v5.

Scenarios (all one replica):

- capacity: `3000:20s,4000:20s,5000:20s,6000:20s,8000:20s,10000:20s`
- higher ladder: `8000:20s,10000:20s,12000:20s,14000:20s,16000:20s`
- slow dependency: `400:40s` with the pricing service at 1,500 ms
- spike: `800:20s,4000:30s,800:30s`

SLO: p99 < 200 ms and < 1% errors (404 counts as OK). "App CPU" is the mean
of the `docker stats` samples inside a phase (about five samples per 20 s
phase, so differences under 3% are noise); divided by the rate it gives CPU
ms per request. Results directories are `harness/results/dev-*`.

## Where the time went before the changes

A 20 s CPU profile of the unchanged service at 5,000 req/s
(`harness/profiles/cpu.pprof`) shows 48% of CPU in network syscalls, 8% in
futex/scheduler, 7% in pgx receive, 7% in malloc/GC and 2% in JSON. Under
`withConn` (39% of CPU) the second query of every read, `loadItems`, alone
accounted for 22%: each read was two round trips to Postgres, and on this
host every socket write is expensive.

The 503s that appear at 6,000 req/s were not the database pool. 15% of the
mix are quotes with 3 SKUs each, so 6,000 req/s is 2,700 pricing calls/s; at
about 25 ms per call that is 67 calls in flight on average (Little's law),
and the process-wide pricing bulkhead was 64 slots with a 100 ms wait. That
is why quote p50 jumped from 30 ms to 123 ms at 6,000 req/s (every call
queued on the semaphore) and why the excess failed with 503.

## Baseline (unchanged go-tuned, `results/dev-high-0`)

| Rate (req/s) | p50 ms | p99 ms | errors | app CPU | CPU ms/req |
|---|---|---|---|---|---|
| 3,000 | 0.99 | 31.8 | 0 | 92% | 0.307 |
| 4,000 | 0.98 | 32.0 | 0 | 116% | 0.290 |
| 5,000 | 0.98 | 39.3 | 0 | 139% | 0.278 |
| 6,000 | 1.01 | 129.5 | 1.13% (503) | 154% | |
| 8,000 | 1.43 | 131.8 | 4.73% (503) | 180% | |
| 10,000 | 11.59 | 145.0 | 21.96% (503) | 199% | |

This matches the recorded go-with medians (capacity 5,000; 1.3% errors at
6,000; 4.8% at 8,000; 17% at 10,000; 0.305 ms CPU per request at 3,000).

## Kept changes

### 1. One query per read: order and items in one round trip (`db.go`)

`GET /orders/{id}` and `GET /customers/{id}/orders` ran two statements
(orders, then `order_items WHERE order_id = ANY($1)`). They now run one:
the page (or the single order) is a subquery with the keyset `WHERE`, `ORDER
BY` and `LIMIT`, joined with `LEFT JOIN LATERAL (SELECT array_agg(sku ORDER
BY sku), array_agg(qty ORDER BY sku) FROM order_items WHERE order_id =
o.id)`. The items arrive as two parallel arrays (`text[]`, `int4[]`) decoded
by pgx in binary, with no JSON. `EXPLAIN ANALYZE` on the seeded database:
nested loop, one index scan of `orders_customer_created` (21 rows) and 21
index scans of `order_items_pkey`, no sort node, 0.2 ms; the single-order
variant is 0.07 ms. The SQL text is assembled from constants only; values
stay `$n` parameters, and rows are still collected with `pgx.CollectRows` and
`pgx.RowToStructByName`. The quote endpoint reuses `getOrder`, so it saves
the round trip too.

Measured alone (`results/dev-high-db1`, pricing client unchanged):

| Rate | app CPU before → after | Postgres CPU before → after | errors before → after |
|---|---|---|---|
| 3,000 | 92% → 77% (0.307 → 0.257 CPU ms/req, −16%) | 53% → 43% | 0 → 0 |
| 5,000 | 139% → 119% | 93% → 76% | 0 → 0 |
| 8,000 | 180% → 159% | 147% → 117% | 4.7% → 4.8% |
| 10,000 | 199% → 186% | 164% → 156% | 22.0% → 7.4% |

The 503s at 6,000 and 8,000 did not move because they come from the pricing
bulkhead (change 2). Fewer statements also means fewer protocol messages on
the Postgres side, which is why Postgres CPU dropped by about 20%.

### 2. Pricing client: bulkhead sized by Little's law, fail-fast, per-call budget, circuit breaker (`pricing.go`)

- `pricingMaxInFlight` 64 → 256, and `MaxIdleConnsPerHost` with it. The
  comment in the code shows the arithmetic: at 10,000 req/s a replica makes
  about 4,500 pricing calls/s, which at 30 ms is 135 in flight.
- The bulkhead no longer waits 100 ms for a slot; a full bulkhead fails at
  once (`overload-protection` rule 6). At this size it never filled in any
  run (zero "too many in-flight calls" errors in the app logs).
- Each call runs under its own `priceTimeout` of 1 s
  (`context.WithTimeoutCause` with `errPriceBudget`), inside the unchanged
  2.5 s quote budget. A call slower than that is a failure, not a wait.
- A circuit breaker opens after 5 consecutive failed calls (timeouts,
  transport errors, 5xx, bad bodies). A 404 is an answer and counts as a
  success; a call that failed only because its quote was cancelled is not
  counted. While open, quotes fail at once with 503 and `Retry-After`. After
  2 s it admits one probe and re-arms the open period at the same moment, so a
  probe whose result is never reported cannot wedge it; a successful probe
  closes it. It is 50 lines of `sync.Mutex` and `time`.

Measured with change 1, three runs (`results/dev-high-dbpr1`, `dev-high-2`,
`dev-high-3`; medians, with the range where it matters):

| Rate | p50 ms | p99 ms | quote p50 / p99 ms | errors | app CPU | CPU ms/req |
|---|---|---|---|---|---|---|
| 3,000 | 0.91 | 31.8 | 29.7 / 32.4 | 0 | 77% | 0.257 |
| 4,000 | 0.85 | 31.7 | 29.6 / 32.1 | 0 | 98% | 0.245 |
| 5,000 | 0.84 | 31.6 | 29.6 / 32.1 | 0 | 116% | 0.232 |
| 6,000 | 0.84 | 31.6 | 29.6 / 32.2 | 0 | 135% | 0.225 |
| 8,000 | 0.86 | 31.8 | 29.7 / 33.4 | 0 | 159% | 0.199 |
| 10,000 | 1.38 | 35.1 (35.0–39.4) | 30.2 / 46.9 | 0.15% (0.09–0.17%, all 503) | 182% | 0.182 |

Every phase of the ladder is inside the SLO in all three runs. The 503s at
10,000 are sheds from the edge limiter (the app logs show no pool or pricing
failures); max latency stays under 100 ms, so nothing waited out the 250 ms
pool acquire.

Higher ladder (`results/dev-higher-dbpr1`):

| Rate | p50 ms | p99 ms | errors | app CPU | Postgres CPU |
|---|---|---|---|---|---|
| 10,000 | 1.47 | 37.9 | 0.17% | 184% | 159% |
| 12,000 | 17.5 | 53.7 | 8.8% (503) | 199% | 171% |
| 14,000 | 19.3 | 57.6 | 30.0% (503) | 200% | 159% |
| 16,000 | 19.4 | 59.6 | 43.6% (503) | 155% | 115% |

The replica saturates its 2 vCPU at about 11,000 req/s. Above that the
excess is shed with fast 503s and p99 stays below 60 ms. From 14,000 the
harness is the bottleneck (nginx at 130% CPU, app CPU falling), so those
phases say nothing about the service.

Slow dependency, pricing at 1,500 ms, two runs (`results/dev-slow-dbpr1`,
`dev-slow-2`) against `results/s4-slowdep-go-with-r1`:

| | before | after |
|---|---|---|
| overall p99 | 1,594 ms | 2.4 ms (2.3–2.4) |
| quote p50 / p99 | 102 / 1,607 ms | 1.2 / 1,002 ms |
| errors | 11.8% (503 after 1.6 s) | 15.2–15.4% (503; 97% of them immediate) |
| other endpoints | unaffected | unaffected |

Of the 2,431 failed quotes in the first run, 2,349 were rejected by the open
breaker in microseconds, 80 waited out the 1 s call budget (the first second
and the probes) and 2 were cancelled. Every quote fails because the
dependency is 1.5 s slow for the whole run; the difference is that each
failure now costs nothing and the other 85% of requests keep their latency.

Spike (`results/dev-spike-1`, `dev-spike-2` against the three
`s3-overload-go-with` runs): at 4,000 req/s p99 is 31.7 ms in both runs with
no errors (go-with: 46.9 ms median, up to 5 503s), app CPU 98% (go-with:
117%), and the 800 req/s phases before and after are identical to go-with.

### Summary of the kept result

| | go-with | go-tuned |
|---|---|---|
| capacity within SLO (one replica) | 5,000 req/s | 10,000 req/s (the top of the ladder; CPU saturates at about 11,000) |
| 6,000 req/s | 1.3% errors, p99 130 ms | 0 errors, p99 31.6 ms |
| 8,000 req/s | 4.8% errors, p99 132 ms | 0 errors, p99 31.8 ms |
| 10,000 req/s | 17% errors, p99 143 ms | 0.15% errors, p99 35 ms |
| 12,000 req/s | | 8.8% errors (fast 503), p99 54 ms |
| CPU per request at 3,000 req/s | 0.305 ms | 0.257 ms (−16%) |
| CPU per request at 5,000 req/s | 0.278 ms | 0.232 ms (−17%) |
| slow pricing (1.5 s): p99 / errors | 1,594 ms / 11.8% | 2.4 ms / 15.3% |
| peak memory per replica | 35 MiB | 34 MiB |

## Tried and not kept

- **PGO (`default.pgo`).** Two 20 s CPU profiles of the tuned service under
  the production mix at 5,000 req/s, merged with `go tool pprof -proto`, and
  confirmed in the binary with `go version -m` (`-pgo=.../default.pgo`).
  Capacity ladder with PGO (`results/dev-high-pgo1`): app CPU 77 / 97 / 117 /
  133 / 159% at 3,000 to 8,000 req/s against 77 / 99 / 116 / 134 / 162%
  without; p99 and error rates identical. That is inside the run-to-run
  spread of identical code, so the profile was removed. The reason is in the
  profile itself: 44% of CPU is still syscalls and 10% futex; the Go code PGO
  can inline is a small share.
- **Adaptive (AIMD) edge limiter.** Not implemented, on the measurements
  above: beyond capacity the fixed 256-slot limiter already sheds with fast
  503s and keeps p99 at 54 ms at 12,000 req/s and 60 ms at 16,000, which is
  the behavior an adaptive limit would be tuned to produce. With this mix the
  only in-flight growth is the CPU saturating, and a latency-driven limit
  would also react to the pricing service's own jitter (quotes are 30 ms,
  reads 1 ms), shedding reads that were not slow. Kept as the
  `overload-protection` "fixed semaphore sized from a load test" option.
- **Lower pool acquire wait (250 ms).** Not changed: in no run did a request
  wait out the acquire (max latency under 110 ms in every capacity phase),
  so the pool was never the bottleneck after change 1.
- **Singleflight across concurrent quotes.** Not implemented. The workload
  has 1,500 distinct SKUs and about 135 pricing calls in flight at 10,000
  req/s, so the chance that an identical lookup is already in flight is under
  10%, and the coordination would cost more than it saves. De-duplicating
  SKUs within one request is moot: the spec rejects duplicate SKUs at
  `POST /orders`.
- **GOGC.** Not tried. `container-runtime-tuning` rule 4 keeps `GOGC` at
  its default with `GOMEMLIMIT` set, and the profile shows GC marking at
  about 3% of CPU (allocation itself is 7%), so the ceiling of that change is
  a few percent.
- **Allocation and JSON work.** Not touched: JSON encoding is 2% of CPU and
  `RowToStructByName` about 3%, and `sql-queries-and-transactions` rule 1
  keeps the named-struct scan.

## Rules this suggests for the skills

- `sql-queries-and-transactions`, rule 4: when the child rows per parent are
  small and bounded (order items), load the page and its children in one
  statement with `LEFT JOIN LATERAL ... array_agg(...)` instead of a second
  `= ANY($1)` query; `= ANY` still costs a second round trip and a second
  wait on the connection. Measured: −16% CPU per request, −20% Postgres CPU.
- `overload-protection`, rule 6: size a bulkhead from Little's law (expected
  call rate × call latency, with headroom) and write the arithmetic in the
  comment; a bulkhead with a timed wait is a queue, and one sized by guess
  (64) was the only cause of 503s at 6,000 req/s.
- `overload-protection` or `outbound-http-client`, new rule: give each call
  to a dependency that is "sometimes much slower" its own budget near its
  normal latency and a circuit breaker with a single half-open probe, so a
  slow dependency fails fast (p99 1,594 ms → 2.4 ms here) instead of being
  waited for up to the request budget.
- `context-and-deadlines`, rule 2: derive the per-call budget from the
  dependency's normal latency, not from the request budget; a 2.5 s quote
  budget let a 1.5 s pricing service hold goroutines, connections and the
  client for the whole time.
- `performance-profiling`, rule 2: when a profile shows most CPU in socket
  syscalls, count round trips per request before touching allocations, and
  do not expect PGO to help until Go code, not syscalls, dominates the
  profile.
