---
name: scale-to-zero
description: Use when writing or reviewing a Go service or function that runs on AWS Lambda, Cloud Run, Knative, Fly Machines or a Kubernetes deployment that scales to zero, when deciding what happens in init(), package-level vars or main before the first request, when a dependency should be built lazily, or when a cold start is slow or billed. Covers no I/O in init or package vars, sync.OnceValue/OnceValues for optional dependencies, a lazy database pool with small MinConns and a short connect timeout, SIGTERM/SIGINT handling inside the platform grace period, measuring startup with GODEBUG=inittrace=1 and Lambda Init Duration, and Lambda builds for provided.al2023 on arm64 with lambda.norpc.
metadata:
  verified: 2026-10-17
---

# Scale to zero

## The decision

On a platform that starts an instance when a request arrives and stops it when traffic goes away, everything the process does before it can answer is paid on the request path, and often on the bill. Go starts fast by default: a static binary with no VM to warm up. Services lose that by doing work before `main` (a package-level `var` that parses every template, an `init()` that fetches configuration or dials the database), by building every dependency eagerly in `main` whether this request needs it or not, and by exiting on SIGTERM without letting in-flight requests finish. The failures are a slow or crash-looping cold start when a dependency blips, connections opened by every new instance, and dropped requests every time the platform scales down. This skill fixes what runs at startup, how optional dependencies are built, and how the process stops on ephemeral platforms. The HTTP shutdown sequence itself (readiness, drain, `Shutdown`) is in `http-server`; the image, `GOMAXPROCS` and `GOMEMLIMIT` are in `container-runtime-tuning`; pool sizing is in `postgres-pgx-pool`.

## The rule

1. Keep `init()` functions and package-level variable initializers free of network calls, file or secret reads, and work that only some requests need.
2. Start listening (or call `lambda.Start`) before building optional dependencies, and build each one on first use behind `sync.OnceValue` or `sync.OnceValues`.
3. Keep network I/O out of the function you pass to `sync.OnceValues`, because it caches an error for the life of the instance: construct there, dial later under the request's deadline.
4. Create the database pool without waiting for connections, with `MinConns` 0 and a connect timeout of a few seconds, and never fail startup because the database is unreachable.
5. Stop on both SIGTERM and SIGINT through `signal.NotifyContext`, and bound `Shutdown` by a budget below the platform's signal-to-SIGKILL window, never by exiting from a signal handler.
6. Measure startup with `GODEBUG=inittrace=1` and the platform's own numbers (Lambda's `Init Duration`, the time from `main` to `Listen`), and do not design around published cold-start figures.
7. On Lambda, deploy a `bootstrap` binary built with `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags lambda.norpc` on the `provided.al2023` runtime.

## Why

**Rule 1.** Package initialization runs before `main`, in dependency order, on one goroutine, every time a process starts. Nothing in it can be skipped, timed out or retried: an `init()` that calls `http.Get` blocks the cold start for as long as the network takes, and the only way to report its failure is `log.Fatal` or a panic, which on Cloud Run or Kubernetes becomes a crash loop the moment the config service blips. Test binaries run `init()` too, so the same call fires on every `go test`. CPU-only work hurts more quietly: parsing two hundred templates or compiling a regexp table for a rarely used endpoint is paid by every instance on every scale-up. Values that are cheap and always needed (a `regexp.MustCompile` of one pattern, a lookup table) are fine at package level.

**Rule 2.** The platform considers the instance started when it accepts a connection (Cloud Run requires listening within 4 minutes and sends the request that woke the instance to it; Lambda's INIT phase ends when the runtime asks for the next event). Whatever `main` does before that is latency for the first request. Building optional dependencies on first use means the request that needs the report renderer pays for it once, and the requests that do not never do. `sync.OnceValue` and `sync.OnceValues` (Go 1.21) run the constructor once, make concurrent callers wait for that one run, and return the same result afterwards, with none of the double-checked locking bugs hand-written lazy init has. Do not replace this with a goroutine that warms dependencies in the background after listening: on Cloud Run with request-based billing, CPU is only allocated while requests are being processed, and on Lambda the environment is frozen between invocations, so the warm-up stalls exactly when you expected it to run. Dependencies every request uses (the logger, configuration parsed from the environment, a client struct with no I/O in its constructor) belong in `main`, eagerly.

**Rule 3.** If `f` returns an error, the function from `sync.OnceValues` returns that same error on every later call; if `f` panics, every later call panics with the same value. A constructor that dials and hits a one-second network blip therefore poisons the instance until the platform replaces it. Most Go clients separate construction from connection: `pgxpool.NewWithConfig` "returns without waiting for any connections to be established", `http.Client` and most SDK clients dial on the first call. Put the construction in the once function, where a failure is a configuration error that will not fix itself, and let the first query or request dial under its own context. When a dependency genuinely must do I/O to be built (fetching a secret), keep it out of `sync.OnceValues` and guard it with a mutex that stores only a successful result, so the next request retries.

**Rule 4.** An eager pool turns every scale-up into a burst of connections and every scale-to-zero cycle into a reconnect storm: ten new instances with `MinConns: 10` open a hundred connections as soon as they start, and the pool keeps them while the instance idles. With `MinConns` 0 an instance opens connections only as requests arrive. A pool that waits for the database at startup, or pings it with the default timeout and exits on failure, makes an outage of the database an outage of every new instance, including the ones that would only have served cached or static routes. Set the connect timeout explicitly (`connect_timeout=3` in the DSN, or `ConnConfig.ConnectTimeout`), so the first query fails within seconds instead of holding the request for the whole deadline. On Lambda an execution environment handles one invocation at a time, so its pool needs one or two connections at most; the cross-replica budget and RDS Proxy or PgBouncer are `postgres-pgx-pool` decisions.

**Rule 5.** Scale-to-zero platforms stop instances all the time, not only on deploys. Cloud Run sends SIGTERM and gives the container 10 seconds before SIGKILL. Fly Machines send SIGINT by default (`kill_signal`) and wait `kill_timeout`, 5 seconds by default and at most 300. Kubernetes sends SIGTERM and waits `terminationGracePeriodSeconds`, 30 seconds by default; on Knative and other platforms built on Kubernetes, read the grace period the platform actually configures. Listening for SIGTERM alone misses Fly's default; calling `os.Exit` from the handler resets every in-flight connection. `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` covers both and hands cancellation to `serve`, which calls `Shutdown` with a budget a few seconds below the window, then `Close`, so a stuck request cannot keep the process past SIGKILL with its cleanup unfinished. Close only the dependencies that were built: calling the once function at shutdown would open the pool just to close it, so record the value when it is built.

**Rule 6.** `GODEBUG=inittrace=1` prints one line per package with init work, in the format `init <pkg> @<start> ms, <clock> ms clock, <bytes> bytes, <allocs> allocs`, to standard error; run the binary with it locally and the expensive package shows up at once. On one laptop the bad example prints `init main @3.9 ms, 2.0 ms clock, 898280 bytes, 10244 allocs` with the network calls disabled: two milliseconds of template parsing nobody asked for, on every start, before any I/O. Lambda reports `Init Duration` on the `REPORT` line of a cold invocation, and AWS suggests it to measure INIT cost; on containers, log the time from the start of `main` to `Listen`, as the good example does. Published Go cold-start numbers are mostly vendor posts and blogs without methodology; there is no authoritative benchmark to design against, and your binary, region, memory size and dependencies decide the number, so measure yours.

**Rule 7.** Go on Lambda runs on an OS-only runtime: the `go1.x` managed runtime is deprecated, and AWS documents `provided.al2023` (supported until June 2029) with an executable named `bootstrap` at the root of the zip. The `lambda.norpc` tag removes the RPC component of `aws-lambda-go`, which only the deprecated `go1.x` runtime used, and shrinks the package. OS-only runtimes support arm64, which is usually billed at a lower GB-second rate than x86 (check the pricing page for your region), and `CGO_ENABLED=0` makes the cross-compile work from any machine and removes the dependency on the libc in the runtime image. The billing change matters here less than it seems: since 1 August 2025 AWS bills the INIT phase for every configuration, but it says custom runtimes, container images and provisioned concurrency already included INIT in billed duration, so a Go function on `provided.al2023` was always paying for its init. The cost of rule 1 and rule 2 on Lambda is therefore money as well as latency, and always has been. `-trimpath` and static linking are release-build rules from `ci-guardrails`; a container-image Lambda follows `container-runtime-tuning`. The example does not import `aws-lambda-go`, to stay standard library only; with it, `main` builds the eager dependencies and then calls `lambda.Start(handler)`, and the handler reaches optional ones through the same once functions.

The cost of laziness: the first request that needs a dependency is slower than the rest, and a configuration error shows up on that request instead of at boot. Validate configuration that is cheap to check (an environment variable that must parse) eagerly in `main`, and accept the first-use latency for what is expensive.

## Good example

See [`examples/good/`](examples/good/).

```go
func newApp(dsn string, open func(string) (*store, error)) *app {
	a := &app{report: sync.OnceValues(parseReport)}
	a.store = sync.OnceValues(func() (*store, error) {
		s, err := open(dsn)
		if err == nil {
			a.opened.Store(s)
		}
		return s, err
	})
	return a
}
```

```go
	// Nothing here dials: the store and the report template are built on first use.
	a := newApp(getenv("DATABASE_URL"), openStore)
	defer a.close()
```

`main` listens before any dependency exists; the store and the report template are built by the first request that needs them, exactly once. The tests count constructor calls: zero after startup and `/healthz`, one after fifty concurrent requests; a broken store fails `/items` with 503 while `/healthz` stays up; and `serve` drains an in-flight request on cancellation and gives up at its budget when a request is stuck. The shutdown tests use a real loopback listener rather than `testing/synctest`, because network I/O is not durably blocking inside a bubble.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
// MISTAKE: a package-level var that does work: all 200 report templates are
// parsed on every cold start, before main runs, though only /report uses them.
var reports = parseAllReports()
```

```go
	// MISTAKE: network I/O in init, before the process listens. On Cloud Run the
	// request that woke the instance waits for it; on Lambda it is billed INIT time.
	resp, err := http.Get("http://config.internal/service.json")
	if err != nil {
		// MISTAKE: a blip in the config service crash-loops every new instance.
		log.Fatal(err)
	}
```

```go
	go func() {
		<-stop
		// MISTAKE: exits at once; in-flight requests get connection resets and
		// the database connections are dropped without being closed.
		os.Exit(0)
	}()
```

This is a long-running service moved to a serverless platform unchanged. The file also opens an eager pool of ten connections with a 30-second dial timeout in `init()`, and listens for SIGTERM only, which never fires on Fly's default SIGINT. The `init()` network code sits behind an environment variable guard so this repository's CI, which builds the package and compiles its tests, never performs it; test binaries run `init()` too, which is one more reason the real code must not do I/O there.

## Checklist

When reviewing code under this skill, confirm:

- [ ] No `init()` and no package-level variable initializer performs network I/O, reads files or secrets, or builds something only some requests use.
- [ ] `main` listens (or calls `lambda.Start`) before constructing any optional dependency.
- [ ] Each optional dependency is built behind `sync.OnceValue` or `sync.OnceValues`, not hand-rolled double-checked locking or a background warm-up goroutine.
- [ ] No function passed to `sync.OnceValues` dials or does other I/O that can fail transiently.
- [ ] The database pool is created without a blocking startup ping that exits on failure, with `MinConns` 0 and an explicit connect timeout.
- [ ] The process handles SIGTERM and SIGINT through `signal.NotifyContext`, and no signal handler calls `os.Exit`.
- [ ] The shutdown budget is below the platform's grace period (Cloud Run 10s, Fly `kill_timeout`, Kubernetes `terminationGracePeriodSeconds`), and `Close` follows a timed-out `Shutdown`.
- [ ] Shutdown closes only dependencies that were actually built.
- [ ] Startup has been measured with `GODEBUG=inittrace=1` and Lambda `Init Duration` or a logged time to `Listen`.
- [ ] Lambda functions ship `bootstrap` on `provided.al2023`, arm64, built with `CGO_ENABLED=0 -trimpath -tags lambda.norpc`.

## When not to apply

- **Always-on services with minimum instances or provisioned concurrency.** When instances are started ahead of traffic (Cloud Run min-instances, Lambda provisioned concurrency, a Kubernetes deployment that never scales to zero), startup is off the request path. Build expensive dependencies eagerly in `main` before reporting ready and fail readiness, not the process, if one is down; the first request is then as fast as the rest. Rules 1 and 5 still apply.
- **A dependency every request uses.** Laziness only moves the cost to the first request. If every handler queries the database, construct the pool in `main` (still without a blocking ping, still `MinConns` 0); the once function adds nothing.
- **Lambda functions without extensions.** AWS gives the runtime 0 ms of shutdown when no extension is registered, 500 ms with an internal extension and 2,000 ms with external ones, and the runtime only gets SIGTERM when an extension is registered. Do not rely on a signal handler to flush buffers or close connections there; flush per invocation, and keep rule 5 for container platforms.
- **Batch jobs and CLIs.** A process that starts, does one job and exits should fail fast: dial the database in `main` with a deadline and exit non-zero if it is down. Lazy initialization only delays the same failure.
- **Fail-fast configuration.** Parsing environment variables, validating a DSN and loading embedded files with `embed` are cheap and should fail at startup, before the platform marks the revision healthy, rather than on the first request.
