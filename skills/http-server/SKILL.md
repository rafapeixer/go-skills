---
name: http-server
description: Use when writing or reviewing a Go HTTP server with net/http: creating the http.Server, registering routes and middleware, reading request bodies, adding health checks, or handling SIGTERM. Covers explicit server timeouts, an own ServeMux with method and wildcard patterns, MaxBytesReader, healthz vs readyz, graceful shutdown order, and http.CrossOriginProtection.
metadata:
  verified: 2026-10-17
---

# HTTP server

## The decision

Every Go service that speaks HTTP decides, usually without noticing, how its `http.Server` is built, where routes live, how much of a request it is willing to read, and what happens between SIGTERM and exit. The standard library's shortcuts make the wrong choice easy: `http.ListenAndServe` has no timeouts, `http.HandleFunc` registers on a global mux that other packages also write to, `r.Body` has no size limit, and a shutdown that looks graceful can still drop every in-flight request. The failures show up only in production: connections held open by slow clients, an out-of-memory kill from one upload, a public `/debug/pprof/`, 502s on every deploy. This skill fixes how the server is assembled, hardened and stopped, using `net/http` alone.

## The rule

1. Build the server as `&http.Server{...}` with `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout` and `IdleTimeout` set, never with `http.ListenAndServe` or a bare `http.Server{Addr: ...}`.
2. Register every route in one `addRoutes` function on your own `http.NewServeMux()`, with `"METHOD /path/{id}"` patterns and `r.PathValue`, never on `http.DefaultServeMux`.
3. Write middleware as `func(http.Handler) http.Handler` and wrap the whole mux once, in the function that builds the handler.
4. Wrap the body in `http.MaxBytesReader` before decoding it, and answer 413 when the error is a `*http.MaxBytesError`.
5. Serve `/healthz` without checking any dependency, and `/readyz` checking dependencies and failing as soon as shutdown begins.
6. On SIGTERM, fail readiness, wait for the load balancer to notice, call `Shutdown` with a `context.WithoutCancel` plus timeout context, and exit only after `Shutdown` returns.
7. Wrap cookie-authenticated browser APIs in `http.NewCrossOriginProtection().Handler`, and never change state on GET, HEAD or OPTIONS.

## Why

**Rule 1.** A zero `http.Server` has no timeouts at all: the docs say a zero value for each means "no timeout". A client that sends one header byte a minute holds a connection and a goroutine forever (Slowloris), and enough of them exhaust file descriptors. `http.ListenAndServe` builds exactly that server, which is why gosec flags it (G114) and a missing `ReadHeaderTimeout` (G112). Each field bounds a different phase: `ReadHeaderTimeout` the request line and headers, `ReadTimeout` the whole request including the body, `WriteTimeout` the response (reset when the next request's headers are read), `IdleTimeout` the wait between keep-alive requests. Note that `WriteTimeout` does not stop the handler; it makes its writes fail, so long handlers still need to watch `r.Context()`. Pick values from your slowest legitimate request, not from a blog post; the example uses 5s, 10s, 15s and 60s.

**Rule 2.** Since Go 1.22, `ServeMux` matches methods (`GET` also matches `HEAD`), single-segment `{id}` and rest-of-path `{path...}` wildcards, and `{$}` for an exact trailing slash. The most specific pattern wins, conflicting patterns panic at registration, and a path that matches with the wrong method gets 405 with an `Allow` header. Hand-written `if r.Method != ...` checks and `strings.TrimPrefix` path parsing redo this worse, usually without `Allow`, and a router library adds a dependency for nothing. `http.DefaultServeMux` is global: `import _ "net/http/pprof"` anywhere in the binary, even in a dependency, registers `/debug/pprof/` on it as a side effect, and your public server starts serving heap dumps (gosec G108). One `addRoutes` function also makes the API surface reviewable in one screen, and the mux's conflict panic fires in the one place you can see both patterns.

**Rule 3.** `func(http.Handler) http.Handler` composes with anything in the standard library, including `CrossOriginProtection.Handler` and `http.TimeoutHandler`, and needs no framework. Middleware that takes dependencies is a function returning that shape (`logRequests(logger)`), so dependencies stay explicit arguments instead of values pulled from a context bag. Wrapping the mux once, in one place, keeps the order visible; middleware applied per route in many files is how an endpoint ends up without logging or CSRF protection.

**Rule 4.** `r.Body` is unbounded. `json.NewDecoder(r.Body).Decode(&v)` on a 4 GB body reads until the process is out of memory, and `ParseMultipartForm` without a limit is the same bug (gosec G120). `MaxBytesReader` returns a `*http.MaxBytesError` once the limit is passed and tells the server to close the connection after the response. Check it with `errors.AsType[*http.MaxBytesError]` so the client gets 413 instead of a misleading 400. The cost is choosing a limit per endpoint; an upload endpoint needs a bigger one than a JSON API, and the default should be small.

**Rule 5.** Liveness answers "is this process stuck?"; a failing liveness probe restarts the container. If `/healthz` pings the database, a 10-second database blip fails liveness on every replica at once and the orchestrator restarts the whole fleet, which turns a blip into an outage. Readiness answers "should this replica get traffic?" and is the right place for dependency checks: a failing readiness probe only takes the replica out of rotation. Readiness must also fail during shutdown, which is what makes rule 6 work.

**Rule 6.** `Shutdown` closes the listeners, then idle connections, then waits for active ones to finish. Two details break naive code. First, `ListenAndServe` and `Serve` return `http.ErrServerClosed` immediately when `Shutdown` is called, so a program that exits when they return, or that treats `ErrServerClosed` as fatal, kills in-flight requests; the docs say to wait for `Shutdown` itself. Second, the signal context is already cancelled when you get to `Shutdown`, so passing it makes `Shutdown` return `context.Canceled` at once. `context.WithoutCancel(ctx)` keeps its values and drops the cancellation, and the timeout bounds the drain. Failing `/readyz` and sleeping a few seconds before `Shutdown` gives the load balancer time to stop sending new requests; without it, requests arrive at a closed listener and the client sees 502s on every deploy. Keep the drain delay plus the shutdown timeout below the orchestrator's grace period (30s by default in Kubernetes), and call `Close` if `Shutdown` times out. `Shutdown` does not wait for hijacked connections such as WebSockets; use `RegisterOnShutdown` to notify them.

**Rule 7.** `http.CrossOriginProtection` (Go 1.25+, so always available on the floor) rejects non-safe cross-origin browser requests with 403, using the `Sec-Fetch-Site` header and, for older browsers, comparing `Origin` with `Host`. It needs no tokens, no cookies and no client changes, which makes CSRF token middleware obsolete. Its limits matter: GET, HEAD and OPTIONS always pass, so a state change on GET is unprotected; requests with neither header pass, because they come from non-browser clients; `Sec-Fetch-Site: same-site` is rejected, so sibling subdomains need `AddTrustedOrigin`. Keep `SameSite=Lax` cookies and HSTS as extra layers. Use `AddInsecureBypassPattern` only for narrow routes such as a webhook that authenticates with a signature.

**Related edge.** If the server also proxies, set `httputil.ReverseProxy.Rewrite`, never `Director`: `Director` is deprecated in Go 1.26 because a client can strip headers it adds by listing them in `Connection`, and it preserves client-sent `X-Forwarded-*` headers. `NewSingleHostReverseProxy` still uses `Director`, so build the `ReverseProxy` yourself with `Rewrite` and `SetURL`.

## Good example

See [`examples/good/`](examples/good/).

```go
// addRoutes is the whole API surface of the service, in one place.
func addRoutes(mux *http.ServeMux, s *store, shuttingDown *atomic.Bool) {
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("GET /readyz", handleReadyz(s, shuttingDown))
	mux.Handle("GET /items/{id}", handleGetItem(s))
	mux.Handle("POST /items", handleCreateItem(s))
}
```

```go
	shuttingDown.Store(true)
	time.Sleep(drain)

	// ctx is already cancelled; WithoutCancel keeps its values, not its Done.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return errors.Join(fmt.Errorf("shutdown: %w", err), srv.Close())
	}
	return nil
```

The routes file is the API surface, and the mux handles methods, path values and 405s. `run` gets its context from `signal.NotifyContext`, and `serve` returns only after `Shutdown` has drained in-flight requests; the test sends a slow request, cancels the context mid-request and checks that it still gets 200.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: no ReadHeaderTimeout, ReadTimeout, WriteTimeout or IdleTimeout,
	// same as http.ListenAndServe; a client sending one header byte a minute
	// holds a connection and a goroutine forever (Slowloris).
	srv := &http.Server{Addr: ":8080"}

	go func() {
		// MISTAKE: ListenAndServe returns ErrServerClosed as soon as Shutdown
		// starts; log.Fatal then exits and cuts off every in-flight request.
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()
```

```go
	// MISTAKE: no readiness flip, so the load balancer keeps sending traffic
	// to a closed listener; and ctx is already cancelled, so Shutdown returns
	// context.Canceled at once instead of waiting for requests to finish.
	srv.Shutdown(ctx)
```

This is the shape of most generated and tutorial servers: global mux, `import _ "net/http/pprof"`, hand-parsed paths, an unbounded `json.NewDecoder(r.Body)`, a delete on GET, a liveness probe that pings the database, and a shutdown that looks graceful but drains nothing. It compiles and passes `go vet`; nothing fails until production traffic or a deploy.

## Checklist

When reviewing code under this skill, confirm:

- [ ] No `http.ListenAndServe`, `http.ListenAndServeTLS` or `http.Server` without `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout` and `IdleTimeout`.
- [ ] Routes are registered on a mux created with `http.NewServeMux()`, never through `http.Handle`, `http.HandleFunc` or a `nil` handler.
- [ ] All routes live in one function, use method and wildcard patterns, and no handler checks `r.Method` or slices `r.URL.Path` by hand.
- [ ] Middleware has the shape `func(http.Handler) http.Handler` and is applied around the mux in one place.
- [ ] Every handler that reads a body wraps it in `http.MaxBytesReader` first and maps `*http.MaxBytesError` to 413.
- [ ] `/healthz` checks no dependency; `/readyz` checks dependencies and returns 503 once shutdown starts.
- [ ] Shutdown flips readiness, waits a drain delay, calls `Shutdown` with a context built from `context.WithoutCancel` plus a timeout, and the process exits only after it returns.
- [ ] `http.ErrServerClosed` is never treated as a failure and never reaches `log.Fatal`.
- [ ] Cookie-authenticated browser endpoints sit behind `http.CrossOriginProtection`, and no GET, HEAD or OPTIONS handler changes state.
- [ ] `net/http/pprof` is not imported into a binary that serves `http.DefaultServeMux`, and debug handlers live on a separate localhost-only listener.

## When not to apply

- **Streaming and long-lived responses** (Server-Sent Events, large downloads, long polling). A server-wide `WriteTimeout` cuts them off. Keep the server timeout for everything else and extend the deadline per request with `http.NewResponseController(w).SetWriteDeadline`, or serve those routes from a second `http.Server` with its own timeouts.
- **Large uploads.** Raise the `MaxBytesReader` limit and `ReadTimeout` for the upload route only (or set the server's `ReadTimeout` to zero and rely on `ReadHeaderTimeout` plus a per-handler deadline), instead of raising the limit for the whole API.
- **Token-authenticated APIs with no cookies.** If every client sends `Authorization: Bearer ...` and the browser never attaches credentials automatically, there is nothing for CSRF to ride on and `CrossOriginProtection` adds nothing; skip it, but keep state changes off GET.
- **No load balancer or orchestrator in front.** A CLI that serves a local UI, or a single VM with no health checks, has nobody to read `/readyz`. Drop the drain delay and the readiness flag, and keep `Shutdown` with a timeout.
- **A router library already in the codebase.** If the project depends on chi or similar for features `ServeMux` lacks (route groups with per-group middleware, regex constraints), keep it; the timeout, body limit, health and shutdown rules still apply unchanged.
- **Tests.** `httptest.NewServer` and `httptest.NewRecorder` need no timeouts or shutdown sequence; build the handler with the same `newHandler` the server uses, so the tests exercise the real routes and middleware.
