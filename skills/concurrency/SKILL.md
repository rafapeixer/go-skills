---
name: concurrency
description: Use when writing or reviewing Go code that starts goroutines, fans work out over a list, runs background workers, shares state between goroutines, or tests concurrent code for leaks. Covers errgroup.WithContext and SetLimit, sync.WaitGroup.Go, goroutine ownership and shutdown, no fire-and-forget in handlers, mutex vs channel, and leak detection with testing/synctest.
metadata:
  verified: 2026-10-17
---

# Concurrency

## The decision

Starting a goroutine costs one keyword, so the real decision is everything around it: how many run at once, who learns that one failed, who tells the others to stop, and who waits for them before the function, the request or the process ends. Get this wrong and the failure is invisible in review and in light testing, then shows up under load: ten thousand goroutines queueing on a pool of twenty database connections, a shared error variable that races, a handler goroutine that dies mid-write on every deploy, or a slow leak that grows memory until the pod is killed. This skill fixes how goroutines are started, bounded, stopped and tested in a Go service.

## The rule

1. Run fan-out whose tasks can fail with `g, ctx := errgroup.WithContext(ctx)`, pass that `ctx` into every task, and return the error from `g.Wait()`.
2. Call `g.SetLimit(n)` whenever the number of tasks comes from input, with `n` well below the size of the pool the tasks share (pgxpool `MaxConns` for database work).
3. Start goroutines that cannot fail with `wg.Go(f)`, never with `wg.Add(1)` inside the goroutine or a hand-paired `Add` and `defer Done`.
4. Give every goroutine an owner that can stop it (a cancelled `ctx` or a closed input channel) and that waits for it to exit before returning.
5. Never start a goroutine in a request handler that outlives the request; do the work inline or hand it to an owned, bounded worker.
6. Guard shared state with a `sync.Mutex`; use a channel only to hand a value, and its ownership, to another goroutine or to signal an event.
7. Test concurrent code inside `synctest.Test`, which fails if any goroutine is left blocked, and check the `goroutineleak` profile in soak tests (Go 1.27+).

## Why

**Rule 1.** The hand-written alternative is `go func()` per task, a `sync.WaitGroup` and a shared `err` variable. The variable is a data race the moment two tasks fail, and when one task fails the others keep running to completion, holding connections and doing work nobody will use. `errgroup.WithContext` records the first error once, cancels the derived context (with that error as its `context.Cause`), and `Wait` returns it after every task has exited. Cancellation only helps if the tasks check it, so pass the derived `ctx` into every call. The derived context is also cancelled when `Wait` returns, so never use it after `Wait`. `errgroup` lives in `golang.org/x/sync` because the standard library has no group that combines errors, cancellation and a limit; the examples are tested with `v0.24.0`.

**Rule 2.** One goroutine per item is fine for three items and a production incident for ten thousand: every one of them asks the database pool for a connection, the pool queues them, each queued request holds memory and a deadline, and the service times out instead of degrading. With `SetLimit(n)`, `g.Go` blocks the producing loop until a slot frees up, which is backpressure for free; there is no need for a hand-written worker pool or semaphore channel. The limit is per group, so concurrent requests each get `n`: keep `n` a fraction of `MaxConns` so that one request cannot take the whole pool, and let the pool remain the global cap. Do not change the limit while tasks are running; `SetLimit` panics if you do.

**Rule 3.** `wg.Add(1)` inside the goroutine races with `wg.Wait()`: `Wait` can see a zero counter and return before the goroutine has been counted. Since Go 1.25, `go vet` reports exactly this ("WaitGroup.Add called from inside new goroutine"), and `wg.Go(f)` (Go 1.25) does the `Add`, the `go` and the `Done` in the right order. `f` must not panic: neither `WaitGroup.Go` nor `errgroup` recovers panics, so a panic in a task crashes the process like a panic in any goroutine. That is deliberate; recover inside the task if it calls code you do not trust.

**Rule 4.** A goroutine nobody can stop runs until the process exits; a goroutine nobody waits for is killed mid-operation at shutdown. The owner is the function or struct that started it: it holds the `cancel` function or the channel it will close, and it calls `Wait` (or `Stop`, which waits) before it returns. A `run` function that starts a background flusher must cancel and wait for it on every return path, and a type that starts a goroutine in its constructor needs a `Stop` or `Close` that waits. The cost is one more method and one more `defer`; the alternative is a shutdown you cannot reason about.

**Rule 5.** `go audit(r.Context(), ...)` in a handler has no owner: the server's `Shutdown` does not wait for it, so it is cut off on every deploy, and nothing bounds how many pile up when the downstream is slow. It usually fails anyway, because `r.Context()` is cancelled as soon as the handler returns. Do short work inline before responding. For work that must survive the response, hand it to a worker that the server's owner starts and stops, through a bounded channel with a non-blocking send, and decide explicitly what happens when the queue is full (drop and count, or return 503). Detaching a context with `context.WithoutCancel` is the `context-and-deadlines` skill's tool for the inline case; it does not give the goroutine an owner.

**Rule 6.** A map or counter read and written by many goroutines is state; a `sync.Mutex` around it is the shortest correct code and the easiest to review. Rebuilding that as a goroutine that owns the map and serves requests over channels adds a goroutine to stop, a protocol to get right, and a deadlock when a caller gives up. Channels are the right tool when a value moves from one goroutine to another and the sender stops touching it (a queue of jobs, a result), or to signal an event (closing a `done` channel). If each goroutine writes only its own slice index, as in the good example, no lock is needed at all.

**Rule 7.** Leaks do not fail tests, they fail pods hours later. `synctest.Test` (Go 1.25) waits for every goroutine started in its bubble to exit and fails the test on deadlock, so a missing `cancel`, a missing `Stop` or a sender stuck on an unbuffered channel becomes a test failure instead of a slow memory climb. It also runs `time.Sleep` and timers on a fake clock, so tests of limits and timeouts are fast and deterministic. Inside the bubble a goroutine blocked on a mutex or real I/O does not count as durably blocked, so keep real network and disk out of those tests. In production, Go 1.27+ ships a `goroutineleak` profile (`/debug/pprof/goroutineleak`) that uses the garbage collector to find goroutines blocked on primitives nothing can reach; serve it only on an internal admin listener and fail soak runs when it is non-empty. It can miss leaks reachable through globals. On Go 1.26 the profile exists only behind `GOEXPERIMENT=goroutineleakprofile` at build time.

## Good example

See [`examples/good/`](examples/good/).

```go
func prices(ctx context.Context, skus []string, lookup lookupFunc) ([]Price, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(maxDBConns)
	out := make([]Price, len(skus))
	for i, sku := range skus {
		g.Go(func() error {
			p, err := lookup(ctx, sku)
			if err != nil {
				return fmt.Errorf("price %q: %w", sku, err)
			}
			out[i] = p // each goroutine writes its own index: no lock needed
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
```

The group bounds the fan-out, the first error cancels the lookups still running, and `Wait` returns only when every goroutine has exited. Background work goes to an auditor that `run` owns: `defer a.Stop()` closes its channel and waits for it, and the tests run inside `synctest.Test`, so a forgotten `Stop` or a sibling that ignores cancellation fails with a deadlock.

```go
// Stop drains the queue and waits for the goroutine to exit. Call it after
// the HTTP server has shut down, so no handler can Record any more.
func (a *auditor) Stop() {
	close(a.events)
	a.wg.Wait()
}
```

## Bad example

See [`examples/bad/`](examples/bad/).

```go
for _, sku := range skus {
	// MISTAKE: one goroutine per input item with no limit; 10,000 SKUs
	// open 10,000 queries against a pool of a few dozen connections.
	go func() {
		// MISTAKE: Add inside the new goroutine races with Wait, which can
		// return before this goroutine is counted. go vet reports it.
		wg.Add(1)
		defer wg.Done()
		p, err := lookup(ctx, sku)
		if err != nil {
			// MISTAKE: unsynchronized write from many goroutines is a data
			// race, and nothing cancels the sibling lookups still running.
			firstErr = err
			return
		}
		out = append(out, p) // MISTAKE: concurrent append loses results or corrupts the slice.
	}()
}
```

```go
// MISTAKE: fire-and-forget. Nobody waits for it at shutdown, there is no
// bound on how many pile up, and r.Context() is cancelled when the handler
// returns, so the audit is usually dropped.
go audit(r.Context(), fmt.Sprintf("priced %d skus", len(ps)))
```

These are the shapes concurrent Go takes when written from memory of older tutorials: `Add` in the wrong place, a shared error variable, unbounded fan-out, a global map with no lock, an unbuffered result channel with an early return (the leak from the Go 1.26 release notes), and a handler that spawns and forgets. The file compiles; `go vet` flags only the `wg.Add` line, and `go test -race` would catch the rest only if a test happened to exercise them concurrently.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Every fan-out whose tasks return errors uses `errgroup.WithContext`, and each task uses the derived `ctx`, not the outer one.
- [ ] Every group over input-sized work calls `SetLimit`, with a limit below the pool size of the resource the tasks share.
- [ ] No `wg.Add` appears inside the goroutine it counts; plain goroutines are started with `wg.Go`.
- [ ] Every goroutine has a visible owner that can cancel it and waits for it on every return path, including errors.
- [ ] Types that start goroutines expose a `Stop` or `Close` that waits, and the owner defers it.
- [ ] No request handler starts a goroutine that is still running after the handler returns.
- [ ] Shared maps, slices and counters are guarded by a mutex (or are atomics); channels carry values or signals, not locks.
- [ ] No send on an unbuffered channel can block forever because the receiver returned early.
- [ ] Tests of concurrent code run inside `synctest.Test`, or otherwise fail when a goroutine outlives the test.
- [ ] CI runs `go vet`, and `go test -race` wherever a C toolchain is available.

## When not to apply

- **Fan-out over a small, fixed set.** Two or three calls known at compile time (fetch the user and their settings) need `errgroup.WithContext` but not `SetLimit`; the number of goroutines cannot grow with input.
- **Tasks that must all run even if one fails.** When a failure must not cancel the others (sending notifications, best-effort cache warming), use a plain `errgroup.Group` without `WithContext`, or `wg.Go` with per-task results, and combine errors with `errors.Join`.
- **CPU-bound work instead of a shared pool.** Image resizing or hashing has no connection pool; bound it by `runtime.GOMAXPROCS(0)`, which respects the cgroup CPU limit on Linux since Go 1.25, instead of by `MaxConns`.
- **Process-lifetime goroutines in `main`.** A goroutine that lives exactly as long as the process (a signal handler started before the server) can be owned by `main` itself; it still exits through the same `ctx` that stops the server.
- **Durable background work.** If the work must survive a crash or a deploy, an in-process worker is the wrong tool even with an owner; write it to a table or queue in the same transaction and process it elsewhere (an outbox).
- **Hot counters.** A single integer incremented on every request is cheaper as an `atomic.Int64` than behind a mutex; switch back to a mutex as soon as two fields must change together.
- **Go 1.26 and the leak profile.** On the floor release the `goroutineleak` profile needs `GOEXPERIMENT=goroutineleakprofile`; rely on `synctest.Test` in tests until you build with Go 1.27+.
