---
name: observability-otel
description: Use when adding or reviewing OpenTelemetry traces, metrics or OTel logs in a Go service, wiring otelhttp into a server or client, naming spans or choosing span and metric attributes, configuring sampling or the Collector's tail sampling, or when the telemetry bill grows. Covers SDK setup and Shutdown in run, otelhttp NewHandler and NewTransport, route-based span names and http.route, cardinality, parent-based and tail sampling, views that drop instruments and attributes, the otelslog bridge, measuring overhead, and why SDK instrumentation beats eBPF auto-instrumentation as a default.
---

# Observability with OpenTelemetry

## The decision

Once a Go service runs behind other services, logs alone cannot say where a slow request spent its time, so you add OpenTelemetry. The decision is not whether to use it but how to wire it so the data is correct and the bill stays bounded. The SDK makes the expensive and the broken choices easy: providers built anywhere and never shut down lose the last batch on every deploy, the default global propagator is a no-op so traces stop at the first hop, a span name built from `r.URL.Path` makes every user ID its own operation, a metric attribute taken from the request turns into one time series per value, and the default sampler records every request. This skill fixes where the SDK is built and flushed, how HTTP is instrumented, what is allowed into names and attributes, how sampling and views cut volume, and how logs join traces. How logs are written (injected logger, redaction, keys, levels) is covered by [`logging-slog`](../logging-slog/SKILL.md); timeouts and shutdown order of the server by [`http-server`](../http-server/SKILL.md).

## The rule

1. Build the tracer, meter and logger providers once in `run`, pass them to the code that instruments, and call their `Shutdown` with a fresh deadline after the HTTP server has drained.
2. Wrap the whole `ServeMux` once with `otelhttp.NewHandler`, wrap every outbound transport with `otelhttp.NewTransport`, and set `propagation.TraceContext{}` explicitly.
3. Name spans and label metrics only with bounded values (the route pattern from `r.Pattern` as `http.route`, the method, the status code), never a raw URL, a user ID or any other request-supplied string.
4. Sample in process with `ParentBased(TraceIDRatioBased(ratio))`, and when you must keep every error and slow trace, export at ratio 1 to a Collector that decides with the `tail_sampling` processor.
5. Register views that drop the instruments and attributes nobody queries (otelhttp's body-size histograms, `server.address`) before they are exported.
6. Log with `log/slog` through the `otelslog` bridge wired in `run`, using the context methods, so every record carries the trace and span ID, and never import the OTel logs API in application code.
7. Measure the instrumentation's cost with a benchmark of the handler with and without `otelhttp`, and run it again on every OTel upgrade.
8. Instrument with the SDK explicitly, and keep eBPF auto-instrumentation (OBI) for services you cannot change.

## Why

**Versions.** Examples pin `go.opentelemetry.io/otel`, `sdk`, `sdk/metric`, `sdk/log` and `log` at v1.47.0, `otelhttp` at v0.72.0 and `otelslog` at v0.21.0. All of them require Go 1.26 (v1.46.0 was the last to support Go 1.25), which matches this repository's floor. The rules about route-based span names need `otelhttp` v0.69.0 or later; earlier versions name the span after the `operation` argument. `WithRouteTag` no longer exists (removed in v0.65.0); do not copy it from old tutorials.

**Rule 1.** The batch span processor exports every 5 seconds and the periodic metric reader every 60 seconds by default, so whatever is buffered when the process exits is lost unless `Shutdown` flushes it. That only works if `Shutdown` actually runs: a `defer tp.Shutdown(...)` in `main` is skipped by `log.Fatal` and `os.Exit`, and never reached when SIGTERM kills a process that does not handle it. Call it from `run`, after `http.Server.Shutdown` has returned so the spans of the last in-flight requests exist, with `context.WithoutCancel(ctx)` plus a timeout because the signal context is already cancelled. Build the providers in one function that takes the span processor, the metric reader and the log processor as arguments: production passes OTLP exporters, tests pass in-memory ones, and nothing else changes. Pass the providers to `otelhttp` with `WithTracerProvider` and `WithMeterProvider` instead of relying on the globals; tests then need no global state and can run in parallel. Still set the globals once in `run`, because third-party instrumentation reads them.

**Rule 2.** `otelhttp.NewHandler` starts a server span, extracts the caller's `traceparent`, and records `http.server.request.duration` and the body-size histograms. Wrap the mux, not each route: the mux sets `r.Pattern`, and since v0.69.0 `otelhttp` renames the span to `{method} {route}` (`GET /users/{id}`) after the handler returns, and adds `http.route` to the metrics. It does not add `http.route` to the span itself, because it builds the span's attributes before the mux has matched; a three-line wrapper inside `otelhttp` that copies `r.Pattern` to the span after `mux.ServeHTTP` fixes that, as the example shows. `otelhttp.NewTransport` starts a client span and injects `traceparent`, so the next service continues the same trace and inherits its sampling decision; a client without it starts a new, unrelated trace on the other side. The global propagator is a no-op until you call `otel.SetTextMapPropagator`, so neither side extracts or injects anything by default. Use `TraceContext{}` alone unless you need baggage: baggage travels to every downstream service, including third parties, and anything put in it leaks with it.

**Rule 3.** Every distinct combination of attribute values is its own time series, and `http.server.request.duration` is a histogram with 15 buckets per series, so a `user_id` attribute multiplies storage by the number of users. Backends bill by active series and slow down as they grow; a scanner probing random paths can create thousands of series in minutes if the path is an attribute. Span names have the same problem in the trace backend: they are grouped and indexed, so `GET /users/42` and `GET /users/43` are two operations, and p99 per endpoint cannot be computed. The route pattern is bounded by the code. A request that matches no route has an empty `r.Pattern`, so its span is named `GET` with no route, and a 404 scan adds nothing. `otelhttp` records `url.path` on the span (not on metrics, and not the query string), which is fine: span attributes are stored per span, not aggregated. If you need to find the traces of one user, put the ID in the log line, which carries the trace ID (rule 6), or in a span attribute that you know no span-to-metrics pipeline turns into a dimension.

**Rule 4.** The SDK's default sampler is `ParentBased(AlwaysSample())`: it records every new trace. `ParentBased` itself is right, because it follows the `sampled` flag of the incoming `traceparent`, so a trace is either recorded by every service or by none. `AlwaysSample` alone ignores the caller and records traces whose parent span was never kept, which leaves orphans. `TraceIDRatioBased` decides from the trace ID, so every service with the same ratio makes the same decision for the same new trace. A head decision is made before the request runs, so it cannot prefer errors or slow requests; that is what the Collector's `tail_sampling` processor does. It buffers spans in memory (`decision_wait` defaults to 30s, `num_traces` to 50000) and then applies policies such as `status_code` and `latency`. Its costs: it is beta for traces and ships in the contrib and k8s Collector distributions, not core; every span of a trace must reach the same Collector instance, which takes a first tier with the `loadbalancing` exporter routing by trace ID; and it can only keep what was exported, so a trace dropped in process never reaches it. Hence the split: without tail sampling, a ratio such as 0.1 cuts the bill; with it, export at 1 and let the Collector drop. An explicit `WithSampler` overrides `OTEL_TRACES_SAMPLER`, so read the ratio from your own config, as the example does.

**Rule 5.** Instrumentation libraries record what the semantic conventions allow, not what you graph. `otelhttp` records request and response body-size histograms for every series; a view with `sdkmetric.AggregationDrop{}` removes them before aggregation, so they cost neither memory nor export. It also sets `server.address` from the request's `Host` header unless you pass `WithServerName`, and a client controls that header. An `AttributeFilter` built with `attribute.NewAllowKeysFilter` keeps the attributes you query (`http.request.method`, `http.route`, `http.response.status_code`, `error.type`) and drops the rest, including any a future version adds. An allow-list is stricter than a deny-list on purpose. The cost is that a dropped attribute cannot be queried later; adding it back is a deploy, and history before it does not exist.

**Rule 6.** The OTel Go Logs API and SDK (`go.opentelemetry.io/otel/log`, `go.opentelemetry.io/otel/sdk/log`) are stable since v1.47.0 (October 2026), joining traces and metrics. The `otelslog` bridge (`go.opentelemetry.io/contrib/bridges/otelslog`) and the OTLP log exporter (`otlploghttp` v0.23.0) are still v0, so they can change between minor releases. Keep them in `run`: application code logs with `*slog.Logger`, and only the handler chain knows about OTel. `otelslog.NewHandler` sends each record to the logger provider, and the SDK takes the trace and span ID from the context passed to `InfoContext` or `LogAttrs(ctx, ...)`; a call without a context produces a record that cannot be joined with its trace. `slog.NewMultiHandler` (Go 1.26) keeps the JSON lines on stdout next to the bridge, so a broken OTLP pipeline does not leave you with no logs at all.

**Rule 7.** There is no current, authoritative number for `otelhttp` overhead, and it depends on your handler, your sampler and your attribute count, so measure it. A benchmark that serves the same request through the bare mux and through `newHandler` gives ns/op and allocs/op for both; run it with `-count 10` and compare with `benchstat` before and after each upgrade. On one development machine, the example measured about 3 µs and 8 allocations bare against 13 µs and 49 allocations instrumented at ratio 1; that is negligible next to a database call and visible on a cache hit, which is why it is measured and not assumed. Profile the instrumented case with `-cpuprofile` if the difference matters.

**Rule 8.** OpenTelemetry eBPF Instrumentation (OBI, which Grafana donated from Beyla) and the OTel Go auto-instrumentation attach to a running binary without code changes. OBI's own README says it is in development and that minor `v0` releases may break configuration and emitted telemetry. Both need Linux and elevated privileges on the host, and they see only what crosses probes they know: no business spans, no attributes from your domain, no views or in-process sampler you control. Explicit SDK instrumentation runs on any platform, is tested with the rest of the code, and is what rules 3 to 5 depend on. Use eBPF for a binary you cannot rebuild, or as a first look at a legacy fleet.

## Good example

See [`examples/good/`](examples/good/).

```go
func newHandler(t *telemetry, prop propagation.TextMapPropagator, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", handleGetUser(logger))
	return otelhttp.NewHandler(withRoute(mux), "server",
		otelhttp.WithTracerProvider(t.tp),
		otelhttp.WithMeterProvider(t.mp),
		otelhttp.WithPropagators(prop),
	)
}
```

```go
	return []sdkmetric.View{
		sdkmetric.NewView(sdkmetric.Instrument{Name: "http.server.request.body.size"}, drop),
		sdkmetric.NewView(sdkmetric.Instrument{Name: "http.server.response.body.size"}, drop),
		// server.address comes from the client's Host header: unbounded.
		sdkmetric.NewView(sdkmetric.Instrument{Name: "http.server.request.duration"},
			sdkmetric.Stream{AttributeFilter: attribute.NewAllowKeysFilter(
				semconv.HTTPRequestMethodKey, semconv.HTTPRouteKey,
				semconv.HTTPResponseStatusCodeKey, semconv.ErrorTypeKey)}),
	}
```

```go
	defer func() {
		// Runs after the HTTP server drained, so the last spans are flushed.
		// ctx is cancelled by now: give the flush its own deadline.
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, tel.Shutdown(flushCtx))
	}()
```

`newTelemetry` builds the three providers with a `ParentBased(TraceIDRatioBased(ratio))` sampler and the views, from processors that `run` creates with OTLP exporters and tests create with `tracetest.InMemoryExporter`, `sdkmetric.ManualReader` and a small in-memory log exporter. The tests, all offline and synchronous, check that two user IDs give one span name and one duration series with `http.route` and no `server.address`, that the body-size histograms are gone, that ratio 0 records only when the caller sampled, that the log record carries the span's trace ID, and that the client sends a `traceparent` with the same trace. `BenchmarkInstrumentation` prices `otelhttp` against the bare mux.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: the user ID and the full URL, query included, become metric
	// attributes; every user is a new time series, billed and kept for
	// months, and reset tokens in the query end up in the metrics backend.
	requests.Add(r.Context(), 1, metric.WithAttributes(
		attribute.String("user_id", r.PathValue("id")),
		attribute.String("url", r.URL.String()),
	))
```

```go
		// MISTAKE: names spans from the raw path; "GET /users/42" and
		// "GET /users/43" are different operations to the backend, so
		// latency per endpoint cannot be grouped and the span-name index grows
		// with every ID and every 404 a scanner tries.
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
```

This is the setup most tutorials and generated code produce: the provider built in `main` with `AlwaysSample` and a `defer Shutdown` that `log.Fatal` skips, no propagator, a span name formatter "for readability", a request counter labelled with the user and the URL, `http.DefaultClient` for the outbound call, `slog.Info` without a context, and no views. It compiles, passes `go vet` and shows traces in a demo. In production it loses the last spans on every deploy, starts a new trace at every hop, and grows the metrics bill with the user count.

## Checklist

When reviewing code under this skill, confirm:

- [ ] Providers are built once in `run` (or a function it calls), and their `Shutdown` runs after `http.Server.Shutdown`, with a context from `context.WithoutCancel` plus a timeout, on every exit path.
- [ ] The server is wrapped once with `otelhttp.NewHandler` around the mux, every outbound `http.Client` uses `otelhttp.NewTransport`, and `propagation.TraceContext{}` is set explicitly.
- [ ] Span names come from the route pattern; no `WithSpanNameFormatter` or `tracer.Start` builds a name from `r.URL`, an ID or other request data.
- [ ] No metric attribute holds a user ID, a raw URL or path, a query value, a `Host` header or any other unbounded value.
- [ ] The sampler is `ParentBased(...)`, never `AlwaysSample()` alone, and the ratio is 1 only when a Collector with `tail_sampling` decides downstream.
- [ ] Views drop the instruments nobody queries and filter `http.server.request.duration` to an allow-list of attributes.
- [ ] Application code logs with `*slog.Logger` and the context methods; only `run` imports `otelslog` or `go.opentelemetry.io/otel/log`.
- [ ] Tests assert span names, attributes and sampling with in-memory exporters and synchronous processors, without a Collector or the network.
- [ ] A benchmark compares the handler with and without `otelhttp`, and an OTel upgrade comes with its result.

## When not to apply

- **CLIs, batch jobs and one-shot tools.** A process that runs for seconds and calls nothing downstream gets little from traces; log a summary line. If a job does fan out, keep rule 1 (`Shutdown` before exit matters even more there, since the process always exits soon after its last span) and sample at 1, because there are few traces.
- **Low traffic.** A service handling a few requests per second can sample at 1 and skip tail sampling; the bill is in the volume, and there is none. Revisit when traffic or the bill grows.
- **No Collector.** Exporting straight from the SDK to a vendor endpoint is fine for a small fleet, but there is nowhere to tail-sample; rely on the in-process ratio and the views, and add a Collector before you need error-biased sampling.
- **A route library already in the codebase.** If the router does not set `r.Pattern`, `otelhttp` cannot see the route; use the router's own OTel middleware or set the span name and `http.route` from its matched template, and keep rules 3 to 5 unchanged.
- **Binaries you cannot rebuild.** For a vendor or legacy binary, eBPF auto-instrumentation is the only option; pin an exact OBI release, review its notes before each upgrade, and do not build alerts on telemetry it marks unstable.
- **Prometheus scraping already in place.** If metrics are scraped with `prometheus/client_golang` and dashboards depend on its names, keep it and add OTel for traces; rule 3 and the allow-list idea apply to Prometheus labels just the same.
