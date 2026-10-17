---
name: outbound-http-client
description: Use when writing or reviewing Go code that calls another service over HTTP with net/http: creating an http.Client or http.Transport, sending requests with Do or Get, reading or closing response bodies, wrapping a third-party API, or fetching a URL that a user supplied. Covers one reused client per upstream, a cloned DefaultTransport with MaxIdleConnsPerHost raised, Client.Timeout plus request context, draining and closing bodies for connection reuse, bounded body reads, transparent gzip, and allow-listing URLs and redirects against SSRF.
metadata:
  verified: 2026-10-17
---

# Outbound HTTP client

## The decision

Every Go service that calls another service decides how its `http.Client` is built, how long a call may take, and what it does with the response body. The standard library makes the dangerous choices the short ones: `http.Get` uses `http.DefaultClient`, which has no timeout; `&http.Client{}` inside a function looks harmless and throws away the connection pool on every call; `defer resp.Body.Close()` looks complete but returns the connection to the pool only if the body was read to the end; `io.ReadAll(resp.Body)` trusts the upstream to send a sane amount; and a URL taken from a request is followed, redirects included, to wherever it points. None of this fails in a unit test against a fast local server. It fails under load (ports in `TIME_WAIT`, handshake CPU), on a slow upstream (goroutines that never return), or in a security review (a request to the cloud metadata endpoint). This skill fixes how the client for one upstream is built and how each call reads its response. How long each call may take within a request is `context-and-deadlines`; retries, backoff and per-dependency concurrency limits are `overload-protection`.

## The rule

1. Create one `*http.Client` per upstream at startup and share it across all requests, never one per call.
2. Build its Transport with `http.DefaultTransport.(*http.Transport).Clone()` and raise `MaxIdleConnsPerHost` from the default 2 to the concurrency you expect toward that host.
3. Set `Client.Timeout` and send every request with `http.NewRequestWithContext` on the caller's context, and never call `http.Get`, `http.Post` or `http.DefaultClient`.
4. After a successful `Do`, `defer` a function that drains the body with `io.Copy(io.Discard, io.LimitReader(resp.Body, n))` and then closes it, so every path, error paths included, gives the connection back.
5. Read response bodies through `io.LimitReader` with a limit one byte above the largest legitimate response, and fail when it is exceeded.
6. Do not set `Accept-Encoding` yourself, because that turns off the Transport's transparent gzip decoding.
7. Check every URL that is not a compile-time constant against an allow-list of scheme and host before dialing, and set `CheckRedirect` so redirects pass the same check.

## Why

**Rule 1.** The `net/http` docs say it directly: the Client's Transport "typically has internal state (cached TCP connections), so Clients should be reused instead of created as needed", and Transports "should only be created once and re-used". A Client with a fresh Transport per call starts with an empty pool, so every call pays a TCP handshake and, for HTTPS, a TLS handshake, which is often more CPU than the request itself. Each finished connection either sits in a pool nobody will use again or closes into `TIME_WAIT` on the client side, and at a few thousand calls a second that exhausts ephemeral ports. A Client per upstream, rather than one for the whole process, lets each upstream have its own timeout, pool size and redirect policy, and makes the dependency an explicit field instead of a global.

**Rule 2.** `DefaultTransport` carries settings a zero `&http.Transport{}` does not: `Proxy: ProxyFromEnvironment`, a dialer with a 30s connect timeout and 30s keep-alive, `ForceAttemptHTTP2`, `MaxIdleConns: 100`, `IdleConnTimeout: 90s`, `TLSHandshakeTimeout: 10s` and `ExpectContinueTimeout: 1s`. A zero Transport has no `IdleConnTimeout` and no dial or TLS handshake timeout. `Clone` gives you a deep copy of the exported fields to change without touching the global. What the defaults get wrong for a service is `MaxIdleConnsPerHost`: when it is zero, `DefaultMaxIdleConnsPerHost` (2) applies. With 50 concurrent calls to one upstream, 48 connections are closed as soon as their call ends and reopened on the next burst, so you pay handshakes under exactly the load where you can least afford them. Size it to the steady-state concurrency toward that host. `MaxConnsPerHost` caps all connections and makes dials block; a concurrency limit belongs in `overload-protection`, where a full limit fails fast instead.

**Rule 3.** `http.DefaultClient` has `Timeout` zero, which the docs define as no timeout, and `http.Get` takes no context. A stalled upstream holds the calling goroutine, its connection and everything above it in the stack until the TCP connection dies, which can be hours. `Client.Timeout` bounds the whole exchange: "connection time, any redirects, and reading the response body", and the timer keeps running after `Do` returns, so a body that trickles in is cut off too. It is the backstop for a call nobody gave a deadline. The request's context carries the actual budget from the incoming request and cancels the call when the client disconnects; `context-and-deadlines` covers how that budget is derived. The two compose: the call ends at whichever comes first. A timeout surfaces as a `*url.Error` whose `Timeout()` reports true, which `errors.AsType[*url.Error]` finds.

**Rule 4.** The `Client.Do` docs: "If the Body is not both read to EOF and closed, the Client's underlying RoundTripper (typically Transport) may not be able to re-use a persistent TCP connection." In Go 1.26 an HTTP/1.1 body closed before EOF makes the Transport close the connection. The common shape is `if resp.StatusCode != 200 { return err }` before any read; every error response then costs a new connection, which turns an upstream's bad minute into a handshake storm on top. The example's test shows it: 50 sequential calls, half of them 503s with a body, open one connection with the deferred drain and 25 without it. Cap the drain (the example uses 64 KiB): reading a multi-megabyte leftover to save one handshake is a bad trade, and past the cap closing the connection is the right outcome. A body that is never closed at all is worse, the connection stays checked out until `Client.Timeout` fires, or forever without one.

**Rule 5.** `io.ReadAll(resp.Body)` reads until the upstream stops, and the upstream may be buggy, compromised or attacker-controlled (rule 7). A 2 GB error page or an endless stream takes the process down. `io.LimitReader(resp.Body, max+1)` lets you tell "exactly at the limit" from "over it" and return an error instead of silently truncated JSON. Pick the limit per endpoint from the largest response it legitimately returns. `json.NewDecoder` over the limited reader works the same way. The limit applies to decoded bytes, so it also bounds a small gzip body that inflates to gigabytes, the decompression bomb gosec G110 warns about for `io.Copy`.

**Rule 6.** The `DisableCompression` docs explain the mechanism: the Transport adds `Accept-Encoding: gzip` only when the request has none, and decodes the response transparently only when it asked for gzip itself. "If the user explicitly requested gzip it is not automatically uncompressed." Setting the header by hand, usually copied from a curl command or "to save bandwidth", gets you the compressed bytes in `resp.Body`, which decode as garbage or fail as invalid JSON, and only when the upstream actually compresses, so tests against an uncompressed fake pass. Leave the header alone and you already get gzip.

**Rule 7.** A URL from a request body, a webhook registration or an upstream's "next page" link can point at `http://169.254.169.254/` (cloud credentials), `localhost:6060/debug/pprof/` or any internal service, and the server fetches it with its own network position: server-side request forgery. gosec flags a request URL built from tainted input (G107, and G704 for the taint-analysis SSRF check). Resolve the reference against the configured base URL and compare scheme and host with an allow-list before the request is built. Redirects need the same check: the default `CheckRedirect` follows up to 10 redirects to any host, so an allowed host that redirects (or is made to redirect) walks the client anywhere. `CheckRedirect` runs before each redirect is followed; return an error to stop it, or `http.ErrUseLastResponse` to hand the 3xx response back to the caller. The Client already drops `Authorization`, `WWW-Authenticate` and `Cookie` on a redirect to a domain that is neither the original nor a subdomain of it; a `CheckRedirect` that copies them back defeats that, which is what gosec G119 reports.

## Good example

See [`examples/good/`](examples/good/).

```go
	c := &Client{base: base}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 64 // default is 2; size it to concurrent calls to this host
	c.hc = &http.Client{
		Transport: t,
		Timeout:   timeout, // whole exchange: dial, TLS, redirects, body read
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("upstream: too many redirects")
			}
			return c.allow(req.URL)
		},
	}
```

```go
	// No Accept-Encoding: the Transport asks for gzip and decodes it for us.
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", u.Path, err)
	}
	defer func() {
		// Drain on every path, error paths included, so the connection is reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		_ = resp.Body.Close()
	}()
```

`upstream.Client` is built once in `main` and wraps the only `*http.Client` for that upstream. `Get` resolves the reference against the base URL, checks it against the same `allow` method the redirect policy uses, sends it on the caller's context and reads the body through a limit. The tests count connections with `httptest.Server.Config.ConnState` (50 sequential calls, half of them error responses, use one connection), and check that an oversized body returns `ErrTooLarge`, a slow upstream returns a timeout `*url.Error` after about 50ms, a redirect to another host and an absolute URL to the metadata endpoint are both refused, and a gzip response is decoded without any header set by hand.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: a new Client and Transport per call; every call pays a TCP and
	// TLS handshake, and a zero Transport has no IdleConnTimeout, so each
	// abandoned pool holds its idle connection until the server hangs up.
	c := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://users.internal/users/"+id, nil)
	if err != nil {
		return nil, err
	}
	// MISTAKE: setting Accept-Encoding by hand turns off transparent gzip;
	// the body below is returned still gzip-compressed.
	req.Header.Set("Accept-Encoding", "gzip")
```

```go
	// MISTAKE: http.Get uses DefaultClient, which has no timeout and no ctx, so
	// a stalled upstream blocks this goroutine forever; and rawURL comes from
	// the user, so http://169.254.169.254/ reads cloud credentials (SSRF, G107).
	resp, err := http.Get(rawURL)
```

The file also returns on a non-200 status without closing the body, reads the success body with an unbounded `io.ReadAll`, closes the avatar body without draining it, and has a shared client whose `CheckRedirect` follows any number of redirects to any host while copying the `Authorization` header onto each one. All of it compiles, passes `go vet`, and works against a healthy upstream; the failures appear under load, on a slow or hostile upstream, or in a security review.

## Checklist

When reviewing code under this skill, confirm:

- [ ] No `http.Get`, `http.Post`, `http.Head`, `http.PostForm` or `http.DefaultClient` anywhere outside tests.
- [ ] Every `http.Client` and `http.Transport` is created once at startup (per upstream) and stored, never inside a function that runs per request or per call.
- [ ] Each Transport is a `Clone()` of `http.DefaultTransport` with `MaxIdleConnsPerHost` set to the expected concurrency, not a zero `&http.Transport{}`.
- [ ] Every Client has a non-zero `Timeout`, and every request is built with `http.NewRequestWithContext` on the caller's context, not `context.Background()`.
- [ ] Right after a successful `Do`, a deferred function drains the body with a capped `io.Copy(io.Discard, io.LimitReader(...))` and closes it, before any status check that can return.
- [ ] Every response body read goes through `io.LimitReader` (or an equivalent cap), and exceeding the cap is an error, not a silent truncation.
- [ ] No code sets `Accept-Encoding` on an outbound request, and nothing decodes gzip by hand that the Transport would have decoded.
- [ ] Every URL that is not a constant is checked against an allow-list of scheme and host before the request is built, and `CheckRedirect` applies the same check with a small redirect cap.
- [ ] No `CheckRedirect` copies `Authorization`, `Cookie` or other credentials onto a redirected request.

## When not to apply

- **Streaming responses** (Server-Sent Events, long downloads, watch APIs). `Client.Timeout` covers the body read, so it cuts a stream off. Use a second Client for those routes with `Timeout` zero, and bound the connection phases on its cloned Transport (`ResponseHeaderTimeout`, the dialer and TLS timeouts) plus a context the caller cancels; read the stream incrementally instead of through a whole-body limit.
- **Truly arbitrary user URLs** (webhook delivery, link previews). A host allow-list cannot list the whole internet. Instead, block private, loopback and link-local addresses at dial time with `net.Dialer.ControlContext` on the cloned Transport, which checks the resolved IP and so also covers DNS names that resolve to internal addresses, and keep the timeout, body limit and redirect cap.
- **One-off CLIs and scripts.** A command that makes three calls and exits gains nothing from pool sizing. Still use a Client with `Timeout` instead of `http.Get`, and still close bodies; skip `MaxIdleConnsPerHost`.
- **Generated or vendor SDK clients.** If the SDK accepts an `*http.Client` or `http.RoundTripper`, pass it the one built here; if it builds its own, configure it through its options and check that it reuses it, rather than wrapping each call in a new SDK client.
- **Intentional raw-body passthrough.** A proxy that forwards a compressed body untouched may set `DisableCompression: true` on its Transport (or forward the client's own `Accept-Encoding`) so it receives the bytes as sent; it must then not parse the body. Proxies are better built with `httputil.ReverseProxy` and `Rewrite`, see `http-server`.
- **Tests.** `httptest.Server.Client()` returns a client already configured for that test server; use it for tests of handlers, and use the production constructor, pointed at `httptest.Server.URL`, for tests of the client itself so the timeout, limits and redirect policy are exercised.
