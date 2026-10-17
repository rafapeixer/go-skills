---
name: logging-slog
description: Use when writing or reviewing logging in a Go service with log/slog, choosing a handler or log level, adding request or trace IDs to logs, logging values that may hold secrets or PII, or logging on a hot path. Covers injecting *slog.Logger, the *Context methods with a context-aware Handler, LogValuer redaction, JSONHandler with a fixed key vocabulary, levels and sampling, LogAttrs, and slog.NewMultiHandler.
metadata:
  verified: 2026-10-17
---

# Logging with slog

## The decision

Every Go service logs, and `log/slog` is the standard way to do it, so the decision is not which library but how logs are wired and what goes into them. The package makes the wrong choices easy: `slog.Info` writes to a global logger from anywhere, the methods without a context compile just as well as the ones with it, `slog.Any("user", u)` prints every field of a struct, and the key/value form accepts any spelling of any key. The failures show up when you need the logs: a bearer token sitting in the log store, an incident where no line carries the request ID, a query on `user_id` that misses half the lines, or a hot path where logging costs more than the work. This skill fixes how the logger is built and passed around, how correlation IDs reach every line, how secrets stay out, and what each level means. When to log an error at all (once, where it stops travelling) is covered by the [`errors`](../errors/SKILL.md) skill.

## The rule

1. Pass a `*slog.Logger` into every constructor that logs, and call `slog.SetDefault` only in `main` or `run`.
2. On request paths, log with the context methods (`InfoContext`, `ErrorContext`, `LogAttrs(ctx, ...)`) and let a wrapping `slog.Handler` add the request or trace ID from the context.
3. Give every type that holds a secret or PII a `LogValue() slog.Value` method that returns a redacted value.
4. Never log `r.Header`, a whole request, a raw body or a full URL, and never put secrets or PII in pprof labels.
5. Use `slog.NewJSONHandler` in production, with snake_case keys from a fixed vocabulary declared once as constants.
6. Log one Info line per request or job at the edge, detail at Debug, handled degradation at Warn, and only failures someone must act on at Error, with the minimum level read from config.
7. Use `LogAttrs` with typed attributes (`slog.String`, `slog.Int64`) on hot paths, and never compute an expensive argument before the level check.
8. Build the handler chain (context handler, sampling, fan-out with `slog.NewMultiHandler`) once in `run`, and sample only Debug and Info.

## Why

**Rule 1.** A package that calls `slog.Info` or `slog.Default()` writes wherever the process-wide logger points. Tests cannot capture its output to assert on it or silence it, and tests that swap the global with `slog.SetDefault` cannot run in parallel without seeing each other's lines. An injected logger is an explicit dependency: a test passes `slog.New(slog.NewJSONHandler(&buf, nil))` and reads the buffer, or `slog.New(slog.DiscardHandler)` to stay quiet. Call `SetDefault` once in `run` anyway: it also routes the old `log` package's default logger through your handler, so dependencies that still call `log.Printf` end up as structured lines instead of plain text on stderr.

**Rule 2.** The built-in handlers ignore the context; it exists so a handler can read values from it. Without the context the handler has nothing to read, and the line cannot be joined with the rest of the request. A wrapping handler that pulls the ID from the context in `Handle` puts it on every line, including lines written deep in the store, without passing the ID as an argument through every function. Its `WithAttrs` and `WithGroup` must return the wrapper again; if they return the inner handler, every logger made with `logger.With(...)` silently drops the ID. Cancelling the context does not stop the line from being written, so logging with `r.Context()` after the client went away is fine. When the service adopts OpenTelemetry, the `otelslog` bridge reads the trace and span IDs from the same context.

**Rule 3.** The built-in handlers resolve a `LogValuer` before writing, so a type that redacts itself stays redacted under any key, through any handler, in any group. Key-based redaction (a `ReplaceAttr` that blanks `password` or `token`) misses the first time someone logs the value as `"creds"` or logs the parent struct; keep it only as a backstop. For a struct, return `slog.GroupValue` with the fields that are safe and leave the rest out, so a field added later is not logged by default. `LogValue` does not affect `fmt` or `encoding/json`, so a secret type also needs a redacting `String` and `json:"-"` (see [`secure-defaults`](../secure-defaults/SKILL.md)). A custom handler that formats values itself must call `Value.Resolve`, or `LogValue` is never called.

**Rule 4.** `r.Header` contains `Authorization` and `Cookie`, so logging it copies every bearer token and session to the log store, where retention is long and access is broad. URLs carry secrets in the query (password reset tokens, OAuth codes, signed links); log `r.Pattern`, the matched route such as `GET /users/{id}`, which is also low-cardinality for queries. pprof labels are not logs, but they leak the same way: they are written into CPU and goroutine profiles, and modules with a `go 1.27` directive also print them in panic tracebacks (Go 1.27+; `GODEBUG=tracebacklabels=0` turns it off). Label by route or tenant tier, never by token or email.

**Rule 5.** `JSONHandler` writes one JSON object per line with escaped values, which every log pipeline parses without a custom grammar; `TextHandler` is for reading in a terminal. JSON alone does not make logs queryable: if one package writes `userId`, another `user-id` and a third `user_id`, a query on one key finds a third of the lines. Declare the keys as constants (`request_id`, `user_id`, `err`, `route`, `status`, `duration_ms`) and put units in the name. `JSONHandler` writes a `time.Duration` as integer nanoseconds and `TextHandler` as `1.5s`, so `slog.Int64("duration_ms", d.Milliseconds())` means the same thing everywhere.

**Rule 6.** Levels are how you reduce volume and how alerts are routed, so each must mean one thing. One Info line per request (method, route, status, duration, request ID) is enough to reconstruct traffic; per-step detail at Info multiplies volume and cost by the number of steps. Error is what pages someone, so an expected outcome (a 404, a validation failure, a retry that succeeded) logged at Error trains people to ignore it. Read the minimum level from config with `Level.UnmarshalText`, and use a `slog.LevelVar` if you need to change it at run time without a restart.

**Rule 7.** `Info(msg, "k", v, ...)` takes `...any`, so each value is boxed and the key/value pairs are parsed on every call. `LogAttrs` with `slog.String` and `slog.Int64` skips both, which is why the package documents it as the efficient form; `go vet` also checks the `...any` form for a missing value, but typed attributes make that mistake impossible. Arguments are evaluated before the logger checks the level, so `logger.Debug("dump", "state", expensiveDump())` pays for the dump with Debug off. Wrap that call in `if logger.Enabled(ctx, slog.LevelDebug)`, or pass a `LogValuer`, whose `LogValue` runs only when the record is actually written. Outside hot paths, the key/value form is fine.

**Rule 8.** Call sites should only say what happened; where the line goes, what is added to it and whether it is kept belong to one place you can read. `slog.NewMultiHandler` (Go 1.26) sends each record to several handlers, such as stdout JSON plus a second sink, without a third-party tee: `Enabled` is true if any handler is enabled, and `Handle` goes only to the handlers enabled for that level. Put the context handler outside the fan-out so every sink gets the ID. Sampling belongs in that chain too, as a wrapper that drops a share of Debug and Info records; sampling with `math/rand` at call sites gives every call site its own policy and usually drops the errors you need. Warn and Error are rare by design (rule 6), so keep all of them.

## Good example

See [`examples/good/`](examples/good/).

```go
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		r.AddAttrs(slog.String(keyRequestID, id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}
```

```go
// LogValue logs only the ID: logging a User can never leak the email or the
// key, even after someone adds a field.
func (u User) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", u.ID))
}
```

```go
func newLogger(level slog.Leveler, w io.Writer, extra ...slog.Handler) *slog.Logger {
	var h slog.Handler = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	if len(extra) > 0 {
		h = slog.NewMultiHandler(append([]slog.Handler{h}, extra...)...)
	}
	return slog.New(contextHandler{h})
}
```

`run` builds the chain once and passes the logger to the handlers; the middleware puts a request ID in the context and writes one `LogAttrs` access line with the route pattern. The test logs into a `bytes.Buffer`, parses each line as JSON, and checks that both lines carry the same `request_id`, that the second sink gets only the Info line, and that neither the API key nor the email appears anywhere in the output.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
		// MISTAKE: logs every header, including Authorization and Cookie,
		// so every bearer token and session cookie ends up in the log store.
		slog.Info("request", "headers", r.Header, "url", r.URL.String())

		// MISTAKE: the token becomes a pprof label; labels show up in CPU and
		// goroutine profiles, and in tracebacks for go 1.27 modules.
		pprof.Do(r.Context(), pprof.Labels("token", r.Header.Get("Authorization")), func(ctx context.Context) {
			u := s.user(ctx, r.PathValue("id"))
			// MISTAKE: no ctx, so a context handler cannot add the request or
			// trace ID; and the whole struct is logged, email and API key included.
			slog.Info("user loaded", "user", u)
			// MISTAKE: a third spelling of the same key; a query on user_id
			// misses "userId" and "user-id".
			slog.Info("responding", "user-id", u.ID)
		})
```

This is how logging looks in most code written quickly: global `slog.Info` everywhere, the headers logged "for debugging", the struct logged whole, keys spelled however the author felt, `TextHandler` left on from local development, and a random sampler around an error. It compiles and passes `go vet`. Running it prints `Authorization:[Bearer sk_live_123]` and `APIKey:sk_live_123` to stdout, and no line has a request ID.

## Checklist

When reviewing code under this skill, confirm:

- [ ] No package other than `main` calls `slog.Default()`, `slog.SetDefault` or the top-level `slog.Info`/`slog.Error` functions; loggers arrive as arguments.
- [ ] Code with a context in scope logs with the `...Context` methods or `LogAttrs(ctx, ...)`, and a wrapping handler adds the request or trace ID.
- [ ] A custom handler's `WithAttrs` and `WithGroup` return the wrapper, not the inner handler.
- [ ] Every type holding a credential, token or PII has a `LogValue` that redacts, and structs log only an allow-list of fields.
- [ ] No log line or pprof label contains `r.Header`, a cookie, an `Authorization` value, a request body or a full URL with its query.
- [ ] Production uses `JSONHandler`, and every key is snake_case from the declared vocabulary, with units in duration and size keys.
- [ ] Each request or job writes one Info line at the edge; expected outcomes are not logged at Error.
- [ ] Hot paths use `LogAttrs` with typed attributes, and no expensive argument is computed outside an `Enabled` check or a `LogValuer`.
- [ ] The handler chain, including any `slog.NewMultiHandler` and sampler, is built in one place, and Warn and Error are never sampled.

## When not to apply

- **CLIs and short scripts.** A tool that runs for seconds and prints to a terminal can use `TextHandler` (or plain `fmt.Fprintln` to stderr) and the default logger; there is no request to correlate and no pipeline to parse JSON.
- **Code with no context in scope.** Startup, config loading and `init`-time work have no request; use `Info` with the injected logger instead of inventing `context.Background()` just to call `InfoContext`.
- **Error storms.** When one dependency fails for every request, thousands of identical Error lines can cost more than the outage tells you. Rate-limit by key in the handler and log the number of suppressed lines, rather than sampling errors at random.
- **Audit logs.** Records you must keep for compliance are not operational logs: write them to their own sink, through their own handler with no sampling and no level filter, and keep their schema separate from the debug vocabulary.
- **Cold paths.** Outside loops and per-request hot code, the `...any` key/value form is easier to read and the allocations do not matter; keep `LogAttrs` for the places a profile points at.
- **Existing zap or zerolog code.** Do not rewrite working logging just to switch packages. Put a `slog.Handler` adapter in front of it for new code, and apply rules 2 to 6 (context, redaction, keys, levels) unchanged.
