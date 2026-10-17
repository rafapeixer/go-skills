// Command bad compiles and exports telemetry, but every marked line is a
// mistake that loses data on exit, breaks traces across services, or turns
// each user and URL into its own span name and metric series.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var requests, _ = otel.Meter("users").Int64Counter("requests")

func handleGetUser(w http.ResponseWriter, r *http.Request) {
	// MISTAKE: the user ID and the full URL, query included, become metric
	// attributes; every user is a new time series, billed and kept for
	// months, and reset tokens in the query end up in the metrics backend.
	requests.Add(r.Context(), 1, metric.WithAttributes(
		attribute.String("user_id", r.PathValue("id")),
		attribute.String("url", r.URL.String()),
	))

	// MISTAKE: http.DefaultClient has no otelhttp transport, so no
	// traceparent goes out; the orders service starts a new, unrelated
	// trace and makes its own sampling decision. It also has no timeout
	// (see outbound-http-client).
	resp, err := http.DefaultClient.Get("http://orders.internal/orders?user=" + r.PathValue("id"))
	if err == nil {
		resp.Body.Close()
	}

	// MISTAKE: no ctx, so the otelslog bridge has no span to read and the
	// line cannot be joined with the trace (see logging-slog).
	slog.Info("user loaded", "user_id", r.PathValue("id"))
	w.WriteHeader(http.StatusOK)
}

func main() {
	exp, err := otlptracehttp.New(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		// MISTAKE: AlwaysSample ignores the caller's decision and records
		// 100% of traffic; the trace bill grows with requests, and traces
		// the caller dropped show up as orphans whose parent was never kept.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	// MISTAKE: this defer never runs: SIGTERM is not handled and log.Fatal
	// calls os.Exit, so the batch processor's buffered spans (up to five
	// seconds' worth) are dropped on every deploy and every crash.
	defer tp.Shutdown(context.Background())
	// MISTAKE: no otel.SetTextMapPropagator; the global propagator is a no-op
	// by default, so incoming traceparent headers are ignored and every
	// request starts a new trace.
	otel.SetTracerProvider(tp)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", handleGetUser)
	h := otelhttp.NewHandler(mux, "server",
		// MISTAKE: names spans from the raw path; "GET /users/42" and
		// "GET /users/43" are different operations to the backend, so
		// latency per endpoint cannot be grouped and the span-name index grows
		// with every ID and every 404 a scanner tries.
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
	// MISTAKE: no views, so otelhttp's request and response body size
	// histograms are exported for every series even though nobody reads them,
	// and server.address carries whatever Host header the client sent.
	srv := &http.Server{
		Addr:              "localhost:8080",
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
