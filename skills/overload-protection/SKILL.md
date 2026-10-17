---
name: overload-protection
description: Use when writing or reviewing Go code that decides what a service does above capacity, adds retries to an outbound call, wraps a dependency, or adds a concurrency or rate limit to an HTTP server. Covers capping in-flight requests with an AIMD or fixed limiter, shedding with an immediate 503 and Retry-After instead of queueing, retrying only idempotent operations with at most 3 attempts and full-jitter backoff, retrying at one layer, a per-client retry budget of about 10%, a fail-fast bulkhead per dependency, and ctx.Err checks before expensive work, all with the standard library.
---

# Overload protection

## The decision

Every service eventually receives more work than it can finish in time: a traffic spike, a deploy that halves capacity, a dependency that slows from 20 ms to 2 s. The decision is what the code does at that moment. The default Go answer is to do nothing: `net/http` starts a goroutine for every connection, a blocking semaphore or pool parks the excess, and each client library retries on its own. The service then queues work its clients have already abandoned, retries multiply that load on the dependency that is already struggling, and the overload sustains itself after the trigger is gone. This skill fixes the shape that prevents it: bound what is in flight, reject the excess fast and cheaply, retry little, at one place, only what is safe to repeat, and isolate each dependency so one slow backend cannot take the others down with it.

## The rule

1. Cap in-flight requests at the edge with a limiter that never waits (AIMD on latency, or at least a fixed semaphore sized from a load test) and answer `503` with `Retry-After` immediately when it is full.
2. Retry only idempotent operations (GET, HEAD, PUT, DELETE, or a write that carries an idempotency key the server honors), and only on transient errors such as 502, 503, 504 or a reset connection.
3. Make at most 3 attempts, wait a full-jitter backoff `rand.N(min(maxDelay, baseDelay<<attempt))` between them, and give up when the context's remaining time is shorter than the next wait.
4. Retry at exactly one layer, the call site closest to the dependency that failed, and return every failure above it unretried.
5. Give each outbound client a retry budget, a token bucket that allows retries for at most about 10% of its requests, and stop retrying when it is empty.
6. Put a bulkhead in front of each dependency, a semaphore sized below that dependency's capacity whose acquire fails fast with `503` instead of waiting.
7. Check `ctx.Err()` before expensive work and pass the request context to every call, so shed and abandoned requests stop using resources.

## Why

**Rule 1.** Goroutines are cheap, so Go servers accept everything, and the queue is invisible: it is the set of goroutines waiting on a mutex, a pool or a slow call. Under overload that queue grows, every request in it waits longer than its client will, and the server spends its capacity on answers nobody reads. A limiter whose acquire never blocks turns the excess into fast `503`s that cost microseconds, and the requests it admits stay fast. The Google SRE book chapter "Addressing Cascading Failures" describes exactly this form of load shedding, returning `HTTP 503` once in-flight requests pass a limit, and recommends small queues (for example 50% or less of the thread pool size) so the server rejects early. `Retry-After` tells well-behaved clients and load balancers when to come back instead of retrying at once.

A fixed limit is wrong after the next deploy, instance size change or dependency slowdown. AIMD (additive increase, multiplicative decrease, the TCP congestion control idea) finds the limit from what the server observes: grow by about one per window of fast responses, cut by a fraction after a slow or failed one. Netflix's `concurrency-limits` library implements this family (Vegas, Gradient2, AIMD) and relates the limit to Little's law, limit = average RPS x average latency. It is a Java library; Go ports exist but are niche, and the limiter in the good example is 50 lines of standard library. Use `503` for "the server is over capacity"; `429` is for a client that exceeded its own quota (Netflix's servlet filter uses `429`, which is fine when the limit is per client).

**Rule 2.** A retry repeats the side effect. A POST that charged a card and then timed out on the response will charge it again. AWS's Builders' Library article "Timeouts, retries, and backoff with jitter" says APIs with side effects are not safe to retry unless they provide idempotency. Errors that will not change on retry (400, 404, a validation error, a permanent auth failure) only add load. Classify transient failures in the client that talks to the dependency, by wrapping them with a sentinel such as `errTransient`, so the retry helper decides with `errors.Is` (see the `errors` skill).

**Rule 3.** The SRE book's "Handling Overload" chapter gives each request "a per-request retry budget of up to three attempts", and "Addressing Cascading Failures" says to always use randomized exponential backoff. Without jitter, clients that failed together retry together, in synchronized waves that hit the dependency exactly when it is trying to recover. Full jitter waits a uniform random time between zero and the capped exponential, which spreads those waves out. A retry that cannot finish before the deadline is wasted work; stopping when the remaining time is shorter than the wait returns the error while the caller can still use it.

**Rule 4.** Retries multiply across layers. The SRE book's example: stacking retries so each of three levels makes four attempts gives 64 (4^3) attempts on the database for one user action. AWS's article gives 3 retries at each layer of a five-deep stack, a 243x load increase on the database. The SRE book says to retry only at the layer immediately above the one rejecting; AWS's best practice for low-cost calls is to retry at a single point in the stack. In Go this means one `retry` call wrapped around the outbound call, and no retry loop in the handler, the service layer, or a retrying `http.RoundTripper` underneath.

**Rule 5.** Three attempts per request still triple the load when everything fails, which is when the dependency can least afford it. The SRE book's per-client retry budget retries a request only while the ratio of retries to requests is below 10%, which it reports cuts the growth from "just below 3X" to "just 1.1x in the general case". AWS's article implements the same idea locally with a token bucket. The good example deposits 0.1 token per call and spends 1 per retry, so retries are capped at about 10% of traffic however long the outage lasts, while a healthy client still retries an occasional blip.

**Rule 6.** One process-wide limit is not enough when there are several dependencies. If search slows to 10 s, its calls hold every in-flight slot, and checkout, which never touches search, starts getting `503`s too. A bulkhead per dependency (a buffered channel used as a semaphore with a `select` and `default`) caps how much of the process one dependency can hold, and failing fast keeps the caller's goroutine free. Size it below what the dependency can serve concurrently. For Postgres the `pgxpool` itself is that bulkhead; see `postgres-pgx-pool` for its short acquire timeout and 503 mapping instead of adding a second semaphore. For the same idea partitioned per tenant at the infrastructure level, see AWS's whitepaper on cell-based architecture and the Builders' Library article on shuffle sharding; both are out of scope here.

**Rule 7.** A shed request costs nothing only if nothing downstream keeps working on it. The SRE book recommends propagating deadlines, checking the remaining time before each stage of a request, and propagating cancellation so downstream servers stop doomed work. In Go that is `ctx.Err()` before an expensive step and the request `ctx` in every call; how to set the edge deadline and derive outbound timeouts is covered in `context-and-deadlines`, and is not repeated here.

The cost: some requests get a `503` that a patient server might eventually have answered, and a few transient failures are returned instead of retried once the budget is spent. That is the trade: a service that degrades by a fraction of its traffic instead of failing all of it, and that recovers when the trigger ends.

## Good example

See [`examples/good/`](examples/good/).

```go
func shed(l *limiter, retryAfter time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.tryAcquire() {
			overloaded(w, retryAfter)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() { l.release(time.Since(start), rec.status >= 500) }()
		next.ServeHTTP(rec, r)
	})
}
```

The middleware never waits for a slot, and the AIMD limiter learns from each response's latency and status. Retries live in one helper, called once, at the call site of the dependency:

```go
	for attempt := range maxAttempts {
		if err = op(ctx); err == nil || !idempotent || !errors.Is(err, errTransient) {
			return err
		}
		if attempt == maxAttempts-1 || !b.trySpend() {
			return err
		}
		// Full jitter: a uniform wait in [0, min(maxDelay, baseDelay*2^attempt)).
		wait := rand.N(min(maxDelay, baseDelay<<attempt))
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= wait {
			return err // the retry could not finish in time anyway
		}
```

The tests run inside `synctest.Test`, whose fake clock only advances when every goroutine in the bubble is durably blocked. That makes "immediately" exact: a shed request and a full bulkhead must return with zero elapsed fake time while two requests are parked in the handler. The retry tests prove three attempts with the backoff under its cap, a single attempt once the budget is spent, and a single attempt for a non-idempotent call, a permanent error and an exhausted deadline, all without real sleeping.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
// MISTAKE: a semaphore with a blocking acquire is a queue, not a limit.
// When it is full, every new request parks a goroutine here until its
// client has long given up, and the backlog keeps the server overloaded.
var inFlight = make(chan struct{}, 100)

func limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight <- struct{}{} // MISTAKE: waits forever, no 503 and no Retry-After
		defer func() { <-inFlight }()
		next.ServeHTTP(w, r)
	})
}
```

```go
	for range 5 { // MISTAKE: 5 attempts per layer; nested twice, inventory gets 25 calls
		if err = op(ctx); err == nil {
			return nil
		}
		// MISTAKE: a fixed sleep, no jitter: every client that failed together
		// retries together, in synchronized waves. It also ignores ctx, so it
		// keeps sleeping and retrying after the request deadline has passed.
		time.Sleep(time.Second)
	}
```

```go
		// MISTAKE: retrying a POST that charges a card, with no idempotency
		// key: a timeout after the charge succeeded bills the customer twice.
		return chargeCard(ctx, id)
```

Each of these looks responsible in review: there is a limit, there are retries, failures are handled. Under load they combine into the failure the skill exists to prevent: the "limit" is a queue, the retry helper is nested inside itself and retries every error, a write is repeated, and one slow backend with no bulkhead holds every slot in the process.

## Checklist

When reviewing code under this skill, confirm:

- [ ] The server caps in-flight requests, and the cap's acquire never blocks: a full limiter answers `503` with `Retry-After` at once.
- [ ] No semaphore, channel or pool in the request path is acquired with a blocking send or an unbounded wait.
- [ ] Every retried operation is idempotent (GET, HEAD, PUT, DELETE, or a write with an idempotency key), and only transient errors are retried.
- [ ] No call is attempted more than 3 times, and waits between attempts use full-jitter exponential backoff, not a fixed `time.Sleep`.
- [ ] Retry waits select on `ctx.Done()`, and no retry starts when the remaining deadline is shorter than the wait.
- [ ] Each outbound call is retried at exactly one layer; there is no retry loop in the handler above it or in a retrying transport below it.
- [ ] Each outbound client has a retry budget (about 10% of its requests) that stops retries when it is spent.
- [ ] Each dependency sits behind its own bulkhead (or its own bounded pool) that fails fast instead of waiting.
- [ ] Handlers check `ctx.Err()` before expensive work and pass the request context to every downstream call.

## When not to apply

- **Batch jobs and queue consumers.** Work pulled from a durable queue has no client waiting and can afford to wait for a slot; bound its concurrency, but block on the bulkhead instead of failing fast, and let the queue's redelivery be the retry layer instead of an in-process loop.
- **Health checks.** Mount `/healthz` outside the shedding middleware: a liveness probe that gets `503` under load makes the orchestrator restart pods exactly when capacity is short.
- **A single-instance internal tool with a handful of users.** A fixed semaphore that sheds with `503` is enough; skip AIMD until load or capacity actually varies.
- **When an upstream proxy already sheds or retries.** If a service mesh or load balancer enforces concurrency limits or retries, do not add a second retry layer in Go; keep the bulkheads and `ctx.Err()` checks, which the proxy cannot do for you.
- **Postgres through pgxpool.** The pool is the bulkhead; follow `postgres-pgx-pool` (short acquire deadline, 503 on exhaustion) instead of wrapping it in another semaphore.
- **Per-tenant fairness.** One noisy tenant can still consume the global limit; when that matters, partition limits per tenant or move to cells or shuffle sharding at the infrastructure level, which this skill only points to.
- **Advanced queueing.** Adaptive LIFO or CoDel request queues (the SRE book mentions LIFO; Facebook described adaptive LIFO with CoDel in "Fail at Scale") can beat plain shedding for bursty traffic, but they are not a default; start with the non-waiting limiter.
