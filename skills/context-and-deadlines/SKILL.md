---
name: context-and-deadlines
description: Use when writing or reviewing Go code that accepts, creates, passes or stores a context.Context, sets timeouts on requests or outbound calls, runs work after an HTTP response, or tests timeout behavior. Covers ctx as first parameter, request deadlines that outbound calls derive from, WithTimeoutCause and context.Cause, WithoutCancel for post-response work, AfterFunc, ctx.Err checks, and t.Context with testing/synctest.
---

# Context and deadlines

## The decision

Every request a Go service handles has a point after which the answer is useless: the client timed out, the user closed the tab, the load balancer gave up. The decision is how that point travels through your code, so that each database query, HTTP call and loop stops when it is reached, and so that the few pieces of work that must outlive it do so on purpose. Get it wrong and the service keeps working for clients that are gone: goroutines pile up behind a slow dependency, the pool drains, and the logs say only `context deadline exceeded` with no hint of which budget ran out. This skill fixes where a context comes from, how it is passed, and how its deadline is set, read and tested.

## The rule

1. Pass `ctx context.Context` as the first parameter of every function that does I/O or can block, and never store it in a struct.
2. Give every request a deadline at the edge, and derive every outbound call's context from the request's context, never from `context.Background()`.
3. Create deadlines with `context.WithTimeoutCause` or `context.WithDeadlineCause`, `defer cancel()` right away, and surface `context.Cause(ctx)` in the returned error.
4. Run work that must outlive the response on `context.WithoutCancel(ctx)` plus its own timeout, in a goroutine with an owner that waits for it.
5. To unblock an API that takes no context, use `stop := context.AfterFunc(ctx, f); defer stop()`, not a goroutine waiting on `<-ctx.Done()`.
6. Check `ctx.Err()` before starting expensive work and on each iteration of a loop that calls out.
7. In tests, use `t.Context()` as the root context and test deadline behavior inside `synctest.Test`.

## Why

**Rule 1.** A context belongs to one call tree: one request, one job, one shutdown. Stored in a struct, it is captured once (often `context.Background()` at construction) and every later call uses it, so a client disconnect or a deadline never reaches the work. Passing it first, by convention, makes the flow visible in every signature, so a reviewer sees where it is dropped. The Go team's guidance and the `context` package docs say the same; the only accepted exception is a type that represents a single operation, see below.

**Rule 2.** `net/http` cancels `r.Context()` when the client disconnects or the handler returns, but it never sets a deadline on it. `http.Server.WriteTimeout` does not help: it makes writes after the deadline fail, and the handler keeps running with an uncancelled context. A middleware that wraps every request in `WithTimeoutCause` gives the request a budget. Outbound calls then derive from that context, often with a tighter per-call budget, so the call cannot outlive the request. A call made on `context.Background()` ignores both the budget and the disconnect, and it loses the trace ID and other request values.

**Rule 3.** When a timer fires, `ctx.Err()` is always `context.DeadlineExceeded`. With three nested budgets (request, price lookup, database acquire), that error does not say which one expired. `WithTimeoutCause(ctx, d, errPriceBudget)` makes `context.Cause(ctx)` return `errPriceBudget`; a cause set by an ancestor propagates to every child, so code that returns `context.Cause(ctx)` reports the budget that actually ran out. `ctx.Err()` is unchanged, so `errors.Is(err, context.DeadlineExceeded)` still works on the plain error. The `cancel` returned by `WithTimeout*` releases the timer and the parent's reference; discarding it keeps both alive until the deadline, and `go vet` reports it (`lostcancel`). Note that calling `cancel` does not set your cause: a context cancelled that way reports `context.Canceled`.

**Rule 4.** Audit writes, cache fills and outbox inserts often run after the response is written. On `r.Context()` they are cancelled the moment the handler returns, so they fail intermittently and only under real traffic. On `context.Background()` they lose request values (trace ID, tenant) and have no limit at all. `context.WithoutCancel(ctx)` keeps the values and drops the cancellation; wrapping it in `WithTimeout` restores a bound. The goroutine needs an owner (a `sync.WaitGroup` waited on at shutdown, or a job queue), or shutdown kills it mid-write.

**Rule 5.** `net.Conn.Read`, `sync.Cond.Wait` and many legacy client libraries take no context. The usual workaround, `go func() { <-ctx.Done(); conn.Close() }()`, leaks one goroutine for every call whose context is never cancelled, which is every call on a long-lived or `Background` context. For standard-library contexts, `context.AfterFunc` registers the callback without starting a goroutine until the context is done, and `stop()` removes it when the call returns normally.

**Rule 6.** Cancellation is cooperative: a context only stops code that looks at it. Blocking calls that take `ctx` check it for you, but CPU-bound work and loops between calls do not. A `ctx.Err()` check before each expensive step costs nanoseconds and turns "finish all 500 items for a client that left" into "stop at the next item".

**Rule 7.** `t.Context()` (Go 1.24) is cancelled when the test finishes, before `Cleanup` functions run, so goroutines the test started stop instead of leaking into the next test. `context.Background()` in a test never cancels. Deadline tests on the real clock either sleep for the full timeout (slow) or use tiny timeouts that flake on a loaded CI machine. Inside `synctest.Test` (Go 1.25) the clock is fake and advances only when every goroutine in the bubble is durably blocked, so a 500 ms timeout takes no wall time and `time.Since(start)` is exactly 500 ms.

The cost: every signature grows a parameter, every budget needs a named cause, and post-response work needs an owner. That is the price of a service whose work stops when the client stops waiting, and whose timeout logs say which dependency was slow.

## Good example

See [`examples/good/`](examples/good/).

```go
func (s *server) price(ctx context.Context, sku string) (int, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, 500*time.Millisecond, errPriceBudget)
	defer cancel()
	p, err := s.prices.Price(ctx, sku)
	if err != nil {
		return 0, fmt.Errorf("price %s: %w", sku, err)
	}
	return p, nil
}

	s.wg.Go(func() {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		s.audit.Record(actx, skus)
	})
```

The per-call budget derives from the request's context, which a middleware already bounded with `errRequestBudget`, so whichever budget expires first is the cause the caller sees. The audit keeps the request's values, drops its cancellation, and runs under its own one-second limit on a `WaitGroup` that shutdown waits for. The tests run inside `synctest.Test` and assert that the lookup stops at exactly 500 ms of fake time, that the request budget wins over a slower sequence of calls, and that the audit still lands after the handler returns. `readLine` in `deps.go` shows `context.AfterFunc` unblocking a `net.Conn` read.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
func (s *server) quote(skus []string) (int, error) {
	total := 0
	for _, sku := range skus {
		// MISTAKE: no ctx.Err() check, so the loop keeps calling the upstream
		// for every SKU after the request is already over.
		// MISTAKE: plain WithTimeout and a discarded cancel: the timer lives
		// until it fires, and a timeout says only "context deadline exceeded".
		ctx, _ := context.WithTimeout(s.ctx, 500*time.Millisecond)
```

```go
	// MISTAKE: r.Context() is cancelled as soon as this handler returns, so the
	// audit write is usually dropped; the goroutine also has no owner to wait on it.
	go s.audit.Record(r.Context(), skus)
```

The file also stores the context in the server struct, calls `context.Background()` inside a handler, leaks a goroutine on `<-ctx.Done()`, and sets no request deadline. Each of these compiles and passes a quick manual test, because the failures only appear under slow dependencies or disconnecting clients. `go vet` flags only the discarded `cancel`.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Every function that does I/O or can block takes `ctx context.Context` as its first parameter.
- [ ] No struct has a `context.Context` field (except a type that represents one operation and does not outlive it).
- [ ] Every request gets a deadline at the edge, and no handler or code below it calls `context.Background()` or `context.TODO()`.
- [ ] Every `WithTimeout`, `WithDeadline` or `WithCancel` is followed by `defer cancel()`, and request or dependency budgets use the `*Cause` variants with a named error.
- [ ] Errors returned on cancellation carry `context.Cause(ctx)`, not only `ctx.Err()`.
- [ ] Work that runs after the response uses `context.WithoutCancel` plus its own timeout, and has an owner that waits for it.
- [ ] No goroutine exists only to wait on `<-ctx.Done()`; such cases use `context.AfterFunc` with `defer stop()`.
- [ ] Loops that call out or do heavy work check `ctx.Err()` on each iteration.
- [ ] Tests use `t.Context()`, and timeout tests run inside `synctest.Test` instead of sleeping on the real clock.

## When not to apply

- **`main` and process roots.** `context.Background()` is correct exactly once: at the root of the process, usually wrapped by `signal.NotifyContext`. Since Go 1.26 that context's cause names the signal, so log `context.Cause(ctx)` on shutdown.
- **Types that represent one operation.** A struct that lives exactly as long as one call (an iterator over one query's rows, a request-scoped decoder) may hold that call's context, the way `http.Request` does. It must not outlive the call or be shared.
- **Background workers and consumers.** A queue consumer has no client and no request deadline. Give each message or job its own `WithTimeoutCause` derived from the worker's root context instead of a per-request budget.
- **Streaming and long-polling handlers.** Server-sent events, WebSockets and large downloads cannot fit a fixed request budget. Skip the deadline middleware for those routes and bound them with an idle timeout or `http.ResponseController.SetWriteDeadline` per write instead.
- **Cheap, internal-only timeouts.** A short timeout on a single local operation whose error never leaves the function does not need a named cause; use `WithTimeout` and `defer cancel()`.
- **Pure functions.** A function that does no I/O, does not block and finishes in microseconds does not take a context; adding one only adds noise.
