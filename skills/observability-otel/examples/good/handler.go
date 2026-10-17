package main

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// newHandler wraps the whole mux once. otelhttp names the span after the
// handler runs, from r.Pattern: "GET /users/{id}", never "GET /users/42".
func newHandler(t *telemetry, prop propagation.TextMapPropagator, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", handleGetUser(logger))
	return otelhttp.NewHandler(withRoute(mux), "server",
		otelhttp.WithTracerProvider(t.tp),
		otelhttp.WithMeterProvider(t.mp),
		otelhttp.WithPropagators(prop),
	)
}

// withRoute copies the matched pattern to the span as http.route. otelhttp
// starts the span before the mux matches, so only the name and the metrics
// get the route by themselves.
func withRoute(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		if i := strings.IndexByte(r.Pattern, '/'); i >= 0 {
			trace.SpanFromContext(r.Context()).SetAttributes(semconv.HTTPRoute(r.Pattern[i:]))
		}
	})
}

func handleGetUser(logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The ID goes in the log line, which carries the trace ID through
		// ctx, not in a span name or a metric attribute.
		logger.InfoContext(r.Context(), "user loaded", "user_id", r.PathValue("id"))
		w.WriteHeader(http.StatusOK)
	})
}

// newClient injects traceparent into every outbound request, so the next
// service continues this trace and honours its sampling decision.
func newClient(t *telemetry, prop propagation.TextMapPropagator) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: otelhttp.NewTransport(http.DefaultTransport.(*http.Transport).Clone(),
			otelhttp.WithTracerProvider(t.tp),
			otelhttp.WithMeterProvider(t.mp),
			otelhttp.WithPropagators(prop),
		),
	}
}
