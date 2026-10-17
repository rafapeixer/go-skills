---
name: performance-profiling
description: Use when a Go service is slow or expensive and someone proposes an optimization, when exposing pprof or debug endpoints, adding a trace or flight recorder, committing or refreshing default.pgo, reading -gcflags=-m output, adding a sync.Pool, or writing benchmarks to justify a performance change. Covers pprof on an internal listener only, CPU and heap profiles before code changes, runtime/trace FlightRecorder snapshots on SLO misses, PGO from merged production profiles, escape analysis, capped pointer-only sync.Pool, and CPU-seconds and bytes allocated per request.
metadata:
  verified: 2026-10-17
---

# Performance profiling

## The decision

When a Go service gets slow or expensive, the decision is what evidence an optimization needs before it is written, and how the service produces that evidence in production without hurting itself. Left alone, people optimize by guess: a `sync.Pool` here, a hand-inlined loop there, a benchmark run once on a laptop. Meanwhile the service either cannot be profiled at all, or it can be profiled by anyone on the internet because `net/http/pprof` was imported next to the public routes. The tail-latency spike that started the work is gone by the time someone attaches a tracer. This skill fixes where profiling endpoints live, which profiles you collect, how you catch rare slow requests, how PGO is fed, and when allocation tricks such as `sync.Pool` are allowed. GC and memory-limit knobs belong to `container-runtime-tuning`; how to write a benchmark belongs to `testing`.

## The rule

1. Serve `net/http/pprof` only from handlers registered explicitly on an internal listener's mux, and never give a server a nil `Handler`, because the import alone registers pprof on `http.DefaultServeMux`.
2. Change code for performance only where a CPU profile or an `alloc_space` heap profile from production-like load points, count the round trips per request (database queries, outbound calls) before working on allocations or expecting a gain from PGO when network syscalls dominate the CPU profile, and prove the change with before and after `b.Loop` benchmarks run with `-benchmem -count=10` and compared with benchstat.
3. Run a `runtime/trace` `FlightRecorder` from startup in every latency-sensitive service and write a rate-limited snapshot when a request misses its latency objective.
4. Commit `default.pgo` in the main package, built from several merged, equal-duration CPU profiles of production, and refresh it on a schedule; never build it from benchmarks.
5. Before restructuring code to save allocations, run `go build -gcflags=-m` on the package the profile names and fix the specific "escapes to heap" lines.
6. Use `sync.Pool` only for pointer types that are fixed-size or capped before `Put`, and only where a profile shows that allocation is a cost.
7. Track CPU-seconds per request and heap bytes allocated per request as the service's efficiency numbers, not raw CPU or RSS.

## Why

**Rule 1.** `net/http/pprof` registers `/debug/pprof/`, `/cmdline`, `/profile`, `/symbol` and `/trace` on `http.DefaultServeMux` in its `init`, and `http.ListenAndServe(addr, nil)` serves exactly that mux. On the public port this hands out the command line (flags, sometimes secrets), heap and goroutine dumps (data and internal structure), and a `?seconds=N` CPU profile or trace that anyone can start in a loop. gosec flags the import as G108, "Profiling endpoint is automatically exposed". Register `pprof.Index`, `pprof.Cmdline`, `pprof.Profile`, `pprof.Symbol` and `pprof.Trace` yourself on a `http.NewServeMux()` that only a second `http.Server` on `127.0.0.1` serves; `pprof.Index` also serves every named profile (`heap`, `allocs`, `goroutine`, `mutex`, `block`) under `/debug/pprof/`, and on Go 1.27+ `goroutineleak` as well. Reach it with `kubectl port-forward`. The import still fills `DefaultServeMux`, so the public server must have its own mux, which `http-server` already requires; a test that the public handler returns 404 for `/debug/pprof/` keeps it that way. `pprof.Profile` extends the connection's write deadline by the requested seconds, so the internal server can keep a normal `WriteTimeout`.

**Rule 2.** Intuition about Go hot paths is usually wrong: the cost is in an encoder, a regexp compiled per call, a map grown from zero, or the allocator, not in the loop someone rewrote. Collect a CPU profile (`go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30`) and a heap profile (`/debug/pprof/allocs`, or `-sample_index=alloc_space` on `heap`) from production or a load test, on demand or from a continuous profiler if you run one. The GC guide says `alloc_space` is "typically the most useful view" for GC cost because it tracks allocation rate, and that more than 15% cumulative time in `runtime.mallocgc` "typically indicates a lot of memory being allocated"; read those with `top -cum`. Then write a benchmark for the function the profile names and compare runs: `b.Loop`, `-count=10` and benchstat are the mechanics from `testing`, and `b.ReportAllocs` or `-benchmem` makes B/op visible, which is the number allocation work is supposed to move. A change that benchstat reports as `~` is not an optimization, it is churn. The cost is an hour of measuring before coding; the alternative is code that is harder to read and no faster.

Read the shape of the profile before picking a target. When most CPU sits in network syscalls (`syscall`, `internal/poll`, `runtime.netpoll`) and the scheduler, the cost is the number of messages the service sends, not the Go code around them: count the round trips each request makes to the database and to other services, and remove one before touching allocations. In the orders-api benchmark (one 2 vCPU / 1 GiB replica, three runs), the profile at 5,000 req/s showed 48% of CPU in network syscalls, 7% in malloc and GC and 2% in JSON, and the second query of every read accounted for 22% on its own; folding it into the first (`sql-queries-and-transactions` rule 4) cut CPU per request by 9%, while allocation work had a ceiling of a few percent. PGO does not help in that case either: a `default.pgo` merged from production-mix profiles of the same service measured within run-to-run noise, because PGO speeds up Go code it can inline and that code was a small share of the profile. Apply rule 4 once Go code, not syscalls, dominates.

**Rule 3.** Tail latency is rare by definition: when the alert fires, the slow request has finished and a fresh CPU profile shows a healthy service. `trace.NewFlightRecorder(trace.FlightRecorderConfig{MinAge, MaxBytes})` (Go 1.25+) keeps a moving window of the execution trace in memory; `Start` it in `main`, and `WriteTo` a file when a request exceeds the objective, then open it with `go tool trace`. The flight recorder post on go.dev advises a `MinAge` of about twice the window you want to see and a bounded `MaxBytes`, and expects "a few MB of trace data" per second, or 10 MB/s for a busy service. Only one goroutine may run `WriteTo` at a time (a second call returns an error), and only one flight recorder may be active per process, so guard the snapshot with `TryLock` and a cooldown: a burst of slow requests then produces one file instead of filling the disk. Never run `trace.Start` from boot to a file instead; that is the unbounded version of the same data. The Go blog cites no overhead figure for the recorder beyond Go 1.21 having "greatly reduced" tracing overhead, so measure it on your service with rule 7's numbers.

**Rule 4.** With a CPU profile named `default.pgo` in the main package directory, `go build` enables PGO automatically, and the PGO docs report about 2 to 14% better performance on a representative set of programs (as of Go 1.22), mostly from inlining hot calls. The docs recommend committing the profile because it is a build input for reproducible builds. A single 30-second profile may miss a workload, so collect several from different instances and merge them with `go tool pprof -proto a.pprof b.pprof > default.pgo`; the merge sums samples regardless of duration, so keep every input the same length or the longer ones dominate. Profiles from binaries already built with PGO are fine. Code drifts away from the profile and the benefit degrades slowly, so a scheduled job (monthly, or every release) fetches fresh profiles and opens a PR. The docs call microbenchmarks "usually bad candidates" because they exercise a sliver of the program. Costs: the first build with a new profile rebuilds the dependency graph, builds are slower, and binaries are slightly larger. Do not commit a profile you have not checked with `go tool pprof -top`; a profile of an idle instance optimizes nothing.

**Rule 5.** The compiler decides what escapes to the heap, and `go build -gcflags=-m ./pkg` prints each decision ("moved to heap: x", "... escapes to heap", "leaking param"); `-gcflags=-m=2` or the GC guide's `-m=3` adds the reasons. In this skill's bad example it reports `buf escapes to heap` on the line that puts a `[]byte` into a pool, which is the allocation the pool was meant to remove. Read it only for the hot functions the profile named: the full output for a package is thousands of lines, most of them irrelevant. Typical fixes are returning values instead of pointers to small structs, preallocating slices whose size is known, and not passing values through `any` or interfaces in a hot loop. Re-run the benchmark from rule 2; escape analysis changes between releases (Go 1.25 stack-allocates more slices), so a trick that helped two versions ago may now be noise.

**Rule 6.** `sync.Pool` treats every item as interchangeable and may drop any of them at any GC. Pool `*bytes.Buffer` without a cap and one large response grows a buffer to megabytes that every later small request then reuses, so the heap stays inflated long after the spike; golang/go#23199 documents this against the package's own example and is still open. The standard library's `fmt` drops buffers above 64 KiB before `Put` for that reason. Put a capacity check before `Put`, or keep one pool per size class. Store pointers: putting a `[]byte` or a struct value into `any` allocates on every `Put` (staticcheck SA6002). A pool is not free either: `Get` and `Put` have their own cost, the code must `Reset` every item, and a pooled object used after `Put` races with whoever gets it next. Use one only where rule 2's heap profile shows the allocation, and keep it if the benchmark shows fewer B/op and no worse ns/op.

**Rule 7.** Total CPU and RSS move with traffic, so a dashboard of either cannot tell a regression from a busy day. Divide by the request rate: CPU-seconds per request is `rate(container CPU seconds) / rate(requests)`, and bytes allocated per request is `rate(/gc/heap/allocs:bytes) / rate(requests)` from `runtime/metrics`. Both stay flat when only traffic changes and jump when a deploy makes each request more expensive, and both are what rules 2, 4 and 6 should move. Export them through the service's metrics pipeline (`observability-otel`); the GC CPU share and heap goal belong to `container-runtime-tuning`. The benchmark's B/op is the same number measured in isolation.

## Good example

See [`examples/good/`](examples/good/).

```go
func newDebugMux(stats *efficiency) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("GET /debug/efficiency", stats.serve)
	return mux
}
```

```go
	fr := trace.NewFlightRecorder(trace.FlightRecorderConfig{
		MinAge:   2 * slo,  // keep about twice the window you want to look at
		MaxBytes: 16 << 20, // a busy service produces about 10 MB of trace per second
	})
	if err := fr.Start(); err != nil {
		return nil, fmt.Errorf("start flight recorder: %w", err)
	}
```

```go
func putBuffer(b *bytes.Buffer) {
	if b.Cap() > maxPooledBuffer {
		return // let the GC take it
	}
	b.Reset()
	bufPool.Put(b)
}
```

`run` starts two servers: the public one with its own mux, wrapped in a request counter and the flight recorder's SLO check, and an internal one on `127.0.0.1:6060` with pprof and the efficiency counters. A slow request triggers a background snapshot guarded by `TryLock` and a one-minute cooldown. Tests prove the public mux returns 404 for every `/debug/` path while `DefaultServeMux` does carry pprof, that a snapshot file is written to `t.TempDir()` and a second one inside the cooldown is not, that an oversized buffer never comes back from the pool, and that the allocation metric exists in `metrics.All()`. The `b.Loop` benchmark reports B/op for one report.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: the blank import registers /debug/pprof/* on http.DefaultServeMux,
	// and ListenAndServe(":8080", nil) below serves that mux to the internet.
	// Anyone can download heap dumps and command lines, and start CPU profiles
	// and traces in a loop (gosec G108).
	_ "net/http/pprof"
```

```go
	w.Write(b.Bytes())
	// MISTAKE: no cap before Put. One request for a million rows grows the buffer
	// to megabytes, and the pool hands that buffer to every later 1 KB request,
	// so the heap stays inflated long after the spike (golang/go#23199).
	bufPool.Put(b)
```

```go
	// MISTAKE: the result is discarded, so the compiler may drop the call and
	// this measures an empty loop. b.Loop keeps the call alive.
	for i := 0; i < b.N; i++ {
		strings.Repeat("row ", 3)
	}
```

The program also pools `[]byte` values, which allocates on every `Put`, writes a full execution trace from boot to disk, times setup inside a `b.N` loop without `ReportAllocs`, and feeds a microbenchmark's CPU profile to `default.pgo`. Every one of them compiles, passes a quick test on a laptop and looks like diligence in review, which is why they ship.

## Checklist

When reviewing code under this skill, confirm:

- [ ] No server has a nil `Handler`, and `/debug/pprof/` is reachable only through a mux served on an internal listener, with a test that the public handler returns 404 for it.
- [ ] Every performance change cites the CPU or `alloc_space` profile that motivated it and a benchstat comparison of `-count=10` runs with allocations reported, and when network syscalls dominate that profile, the round trips per request were counted and reduced before any allocation work.
- [ ] Latency-sensitive services start a `trace.FlightRecorder` in `main` with `MinAge` and `MaxBytes` set, and snapshot it on SLO misses with at most one write in flight and a cooldown.
- [ ] No code calls `trace.Start` from startup to a file or keeps an unbounded trace.
- [ ] `default.pgo` sits in the main package, comes from merged equal-duration production CPU profiles (not benchmarks), and has a refresh job or a documented refresh date.
- [ ] Allocation-motivated rewrites point at specific `-gcflags=-m` "escapes to heap" lines in the hot function, not at general rules of thumb.
- [ ] Every `sync.Pool` stores a pointer type, checks size before `Put` (or holds fixed-size items), resets items, and is justified by a heap profile and a benchmark.
- [ ] Dashboards show CPU-seconds per request and heap bytes allocated per request, so a deploy that makes requests costlier is visible.

## When not to apply

- **CLIs, batch jobs and tests.** There is no long-lived server to attach to; profile with `go test -cpuprofile cpu.out -memprofile mem.out` or `runtime/pprof.StartCPUProfile` to a file for the run. A batch job's own PGO profile can come from a representative production run.
- **Libraries.** Never import `net/http/pprof` or start a flight recorder in a library: the import mutates the caller's `DefaultServeMux`, and only one flight recorder may run per process. Leave both to `main`.
- **A profiler outside the pod.** When a continuous profiler scrapes pprof over the network, bind the internal listener to the pod IP on a separate port, never the public one, and restrict it with a NetworkPolicy or mTLS; loopback only works for `kubectl port-forward`.
- **Services with no latency objective.** A queue worker or nightly job does not need a flight recorder; capture a full trace with `/debug/pprof/trace?seconds=5` when investigating.
- **No production traffic yet.** Ship without `default.pgo` until there is a real workload to profile; a load test that replays production traffic is acceptable, a microbenchmark is not.
- **Low-traffic paths.** A handler that runs a few times per second gains nothing from a pool or an escape-analysis rewrite; keep the plain allocation and spend the effort where the profile is hot.
- **Fixed-size scratch objects.** A pool of `*[4096]byte` or of a struct with no growable fields needs no size cap; the cap rule is for items that grow with input.
