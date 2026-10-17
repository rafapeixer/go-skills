---
name: testing
description: Use when writing or reviewing Go tests for a service, choosing test doubles, testing database code, timeouts, retries or HTTP wiring, or adding fuzz tests and benchmarks. Covers table tests with t.Run, hand-written fakes over consumer-side interfaces, real Postgres instead of sqlmock, testing/synctest for time, end-to-end tests through run(), fuzzing parsers, b.Loop benchmarks with benchstat, and t.Context.
---

# Testing

## The decision

Every Go service needs tests, so the real decision is what each test runs against: real code or a double, a real database or a mock of its driver, the real clock or a fake one, the assembled server or a lone handler. Get it wrong and the suite fails in one of two ways. It is slow and flaky (real sleeps, shared databases, global state swapped by tests), so people stop running it. Or it is green while production breaks, because a mock accepted SQL Postgres rejects, a handler test skipped the router, or a benchmark measured an empty loop. This skill fixes what Go service tests run against and how they are shaped.

## The rule

1. Replace a dependency with a hand-written fake that implements a small interface declared by the consumer, never with a generated mock that asserts calls.
2. Write tests for one function as a table of named cases, each run with `t.Run(tt.name, ...)`, and call `t.Parallel()` in cases that share no state.
3. Test SQL and repository code against a real Postgres (testcontainers-go or a CI service container) with one database per test cloned from a migrated template, never with sqlmock or SQLite.
4. Test timeouts, retries, backoff and tickers inside `synctest.Test`, never with real `time.Sleep`.
5. Write at least one end-to-end test per service that starts it through `run()` and sends real HTTP requests to it.
6. Give every parser or decoder of untrusted input a `FuzzXxx` test seeded with the table cases.
7. Write benchmarks with `for b.Loop()`, run them with `-benchmem -count=10`, and compare runs with benchstat.
8. Get the test's context from `t.Context()`, never from `context.Background()`.

## Why

**Rule 1.** A mock generated from an interface records which methods you expect, in what order, with which arguments. That is a test of the implementation: change a retry policy or batch two calls into one and every mock expectation breaks, while the behavior the caller sees is unchanged. A fake has behavior instead: an in-memory store that keeps what you put in it, a rate source that stalls for the first two calls. Tests then assert on results. Fakes stay small because the interface is declared where it is consumed and holds only the methods that consumer calls; a 30-method interface copied from the producer is what makes people reach for a generator. Never swap a package-level function variable as a seam: tests that overwrite globals cannot run in parallel and leak into each other.

**Rule 2.** Copy-pasted test functions drift: one checks the error, the next forgets, and a new edge case becomes another near-duplicate. A table makes the next case one line and forces the same assertions on every input. `t.Run` gives each case a name, so failures read `TestParseAmount/three_decimals` and `go test -run 'TestParseAmount/three'` reruns one case. Since Go 1.22 each loop iteration has its own `tt`, so `t.Parallel()` inside the case is safe without copying. Leave it out when cases share a fake or a database, and note that `t.Setenv` and `t.Chdir` panic in parallel tests. Failure messages say the input, the result and the expectation: `ParseAmount("1.234") = 0, want error`, not `wrong`.

**Rule 3.** sqlmock checks that your code sent the string you expected, not that Postgres accepts it. It does not catch a misspelled column, a violated constraint, a wrong type in a scan, `NULL` into a non-pointer, a deadlock between two transactions, or a migration that does not match the queries. SQLite as a stand-in has a different dialect, type system and locking. Start Postgres once per package with testcontainers-go (`postgres.Run`, v0.32.0+; it needs Docker) or use a service container in CI, apply migrations once to a template database, and give each test its own copy with `CREATE DATABASE test_x TEMPLATE app_template`. Postgres refuses to copy a template that has open connections, so close the migration connection first. Cloning takes milliseconds, so `t.Parallel()` is allowed, and per-test databases test code that commits or uses several connections, which a rollback-per-test scheme cannot. testcontainers-go's `Snapshot`/`Restore` does the same with a template behind the scenes; never point it at the `postgres` database. Use pgx `>= v5.9.2` as the driver. Third-party packages are justified here because the standard library has no way to run Postgres.

**Rule 4.** A retry test with real sleeps takes as long as the backoff, and a test that asserts "it timed out after about 2s" fails on a loaded CI runner. `synctest.Test(t, f)` (Go 1.25+) runs `f` in a bubble with a fake clock that starts at 2000-01-01 UTC and jumps forward only when every goroutine in the bubble is durably blocked: on `time.Sleep`, on a channel or `select` over channels created in the bubble, on `sync.WaitGroup.Wait` or `sync.Cond.Wait`. Timers, tickers and `context.WithTimeout` all use that clock, so a test of three attempts with 2s timeouts and 1s backoff runs in microseconds and can assert the elapsed time exactly. `synctest.Wait()` blocks until the other goroutines in the bubble are durably blocked, which replaces "sleep a bit and hope the goroutine ran". Limits: mutexes, real I/O and syscalls are not durable blocking, so code waiting on a real socket never advances the clock; use in-memory fakes or `net.Pipe`. Inside the bubble, do not call `t.Run`, `t.Parallel` or `t.Deadline`; put `synctest.Test` inside each `t.Run` case instead. The pre-1.25 `synctest.Run` API behind `GOEXPERIMENT=synctest` was scheduled for removal in Go 1.26; use `synctest.Test`. `synctest.Sleep` and the in-memory `httptest.NewTestServer` need Go 1.27+.

**Rule 5.** A handler called directly with `httptest.NewRecorder` skips the mux, so method patterns, conflicting routes, middleware order, server timeouts and the way `run` wires dependencies are never exercised. A thin `main` that only calls `run(ctx, ...)` makes the whole service startable in a test: open a listener on `127.0.0.1:0`, call `run(t.Context(), ln)` in a goroutine, and send real requests to `ln.Addr()`. Because the listener is open before `run` starts, requests queue instead of failing, and no readiness polling is needed; if `run` opens its own listener, poll `/readyz` instead. One such test per service catches the wiring bugs no unit test can. Keep the rest of the suite as unit tests: end-to-end tests are slower and point less precisely at the cause.

**Rule 6.** Hand-written cases cover the inputs you thought of. A fuzz test checks properties on inputs you did not: it never panics, it rejects negatives, parsing the formatted result gives the same value. Plain `go test` runs the seeds from `f.Add` and `testdata/fuzz/FuzzXxx` as normal tests, so the fuzz target costs nothing in CI; `go test -fuzz=FuzzXxx` explores further, and any failing input it finds is written to `testdata/fuzz/` and from then on runs on every `go test`. Commit those files. Untrusted input means request bodies, query and path parameters, headers, file formats and anything read from the network.

**Rule 7.** With the old `for i := 0; i < b.N; i++` loop, setup before the loop is re-run and timed unless you call `b.ResetTimer`, and a call whose result is discarded can be optimized away, so the benchmark measures nothing. `b.Loop` (Go 1.24+) times only the loop, runs setup once, and keeps the arguments and results of calls in the loop body alive. Since Go 1.26 it no longer prevents inlining in the body, so it reports the allocations the real call site would have. Do not mix `b.Loop` with `b.N` in one benchmark. A single run is noise: run with `-count=10` before and after the change, add `-benchmem` so allocations are visible, and compare with `benchstat old.txt new.txt` (`golang.org/x/perf/cmd/benchstat`), which reports `~` when the difference is not statistically significant. Run benchmarks on a quiet machine, not shared CI runners.

**Rule 8.** `t.Context()` (Go 1.24+) is cancelled when the test ends, just before its `Cleanup` functions run. Goroutines and servers started with it stop at the end of the test, and a `Cleanup` can wait for them to exit. A `context.Background()` never cancels, so whatever the test started keeps running into the next test, holding ports, connections and goroutines. Inside `synctest.Test`, `t.Context()` belongs to the bubble.

The cost: real Postgres needs Docker locally or a service in CI, and fakes are code you maintain. Both are cheaper than a suite that passes while production fails.

## Good example

See [`examples/good/`](examples/good/).

```go
t.Run(tt.name, func(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rates := &fakeRates{hang: tt.hang}
		c := &Converter{Rates: rates, Timeout: 2 * time.Second, Attempts: 3, Backoff: time.Second}
		start := time.Now()

		got, err := c.Convert(t.Context(), 1000, "EUR")
```

```go
errc := make(chan error, 1)
// t.Context is cancelled before Cleanup runs, which shuts the server down.
go func() { errc <- run(t.Context(), ln) }()
t.Cleanup(func() {
	if err := <-errc; err != nil {
		t.Errorf("run() = %v", err)
	}
})
```

Each table case gets its own bubble, so three attempts with 2s timeouts and 1s backoff finish instantly and the test asserts the elapsed time exactly (6s, 8s). The fake implements the one-method `RateSource` interface the converter declares. The end-to-end test starts the service through `run` and talks real HTTP; `parse_test.go` holds the table test, the fuzz target seeded from the same table, and a `b.Loop` benchmark.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
// MISTAKE: swapping a global; a parallel test, or a test that forgets to restore it,
// sees this fake instead of the real function.
fetchRate = func(string) (float64, error) {
	calls++
	if calls < 3 {
		return 0, errors.New("upstream down")
	}
	return 0.5, nil
}
start := time.Now()
got, err := convert(1000, "EUR")
// MISTAKE: the retries really sleep, so this test takes 2s of wall clock, and an
// assertion on elapsed time would be flaky on a loaded CI runner. Use synctest.Test.
```

```go
// MISTAKE: the b.N loop discards the result, so the compiler may delete the call
// and the benchmark measures an empty loop. Use for b.Loop().
for i := 0; i < b.N; i++ {
	parseAmount("1234567.89")
}
```

This is what tests look like when code was written without a seam: a global function swapped in place, retries waited out on the wall clock, one copy-pasted function per input, a handler called without its router, and an untested float parser that turns `"1.15"` into 114 cents. Every test passes, which is the problem.

## Checklist

When reviewing code under this skill, confirm:

- [ ] No test uses a mock framework or a hand-rolled mock that asserts call order or call counts; doubles are fakes behind consumer-side interfaces.
- [ ] No test overwrites a package-level variable or function to inject a double.
- [ ] Tests of one function with several inputs are a table run with `t.Run`, and failure messages show input, got and want.
- [ ] SQL and repository code is tested against a real Postgres with a database per test, not sqlmock or SQLite.
- [ ] No test calls `time.Sleep` or asserts wall-clock durations; time-dependent code runs inside `synctest.Test`.
- [ ] The service has at least one test that starts it through `run()` and sends real HTTP requests.
- [ ] Every parser or decoder of untrusted input has a `FuzzXxx` test seeded with known cases.
- [ ] Benchmarks use `for b.Loop()`, not `b.N`, and performance claims come from `-count=10` runs compared with benchstat.
- [ ] Tests get contexts from `t.Context()`, not `context.Background()` or `context.TODO()`.

## When not to apply

- **Dependencies you do not own and cannot run.** For a third-party HTTP API, test your client against an `httptest.Server` that replays recorded responses, and keep a small, separately tagged contract test that hits the real sandbox, so the fake cannot drift from reality unnoticed.
- **Asserting an interaction is the behavior.** When the point of the code is "send exactly one email" or "never call the payment API twice", a fake that records calls and a test that counts them is correct. Count outcomes, not the order of internal calls.
- **Pure functions with one or two inputs.** A single plain test is clearer than a one-row table; reach for the table at the third case.
- **No Docker and no CI database available.** Keep the SQL tests, guard them with `testing.Short()` or an environment variable that names the database, and make CI fail if they were skipped there. Do not replace them with sqlmock.
- **Code that waits on real I/O.** `synctest` cannot advance time while a goroutine is blocked on a real socket, file or mutex. Inject an in-memory fake or `net.Pipe`, or pass a short timeout and assert on the error, not the duration.
- **Libraries and CLIs with no HTTP server.** Rule 5 becomes one test that calls `run(ctx, args, stdout, stderr)` with real arguments and checks the output and exit error.
- **Parsers that only read trusted, generated input.** A config file your own build writes does not need fuzzing; anything a client can send does.
