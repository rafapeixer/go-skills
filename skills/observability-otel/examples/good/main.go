// Command good instruments an HTTP service with the OpenTelemetry SDK:
// providers built once in run and flushed on exit, otelhttp on the server
// and the client, route-based span names, parent-based sampling, views that
// drop unused metrics, and slog bridged to OTel logs.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

func main() {
	if err := run(context.Background(), os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdout io.Writer) (err error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	ratio, parseErr := strconv.ParseFloat(getenv("TRACE_SAMPLE_RATIO"), 64)
	if parseErr != nil {
		ratio = 0.1 // keep 10% of new traces; the Collector decides the rest
	}
	// The exporters read OTEL_EXPORTER_OTLP_ENDPOINT and connect lazily.
	spanExp, err1 := otlptracehttp.New(ctx)
	metricExp, err2 := otlpmetrichttp.New(ctx)
	logExp, err3 := otlploghttp.New(ctx)
	if err := errors.Join(err1, err2, err3); err != nil {
		return fmt.Errorf("otlp exporters: %w", err)
	}
	res := resource.NewSchemaless(semconv.ServiceName("users"))
	tel := newTelemetry(res, sdktrace.NewBatchSpanProcessor(spanExp),
		sdkmetric.NewPeriodicReader(metricExp), sdklog.NewBatchProcessor(logExp), ratio)
	defer func() {
		// Runs after the HTTP server drained, so the last spans are flushed.
		// ctx is cancelled by now: give the flush its own deadline.
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, tel.Shutdown(flushCtx))
	}()

	// Globals only for dependencies that read them; our code gets tel.
	prop := propagation.TraceContext{}
	otel.SetTracerProvider(tel.tp)
	otel.SetMeterProvider(tel.mp)
	otel.SetTextMapPropagator(prop)

	srv := &http.Server{
		Addr:              "localhost:8080",
		Handler:           newHandler(tel, prop, newLogger(stdout, tel)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newLogger keeps the JSON lines on stdout and also sends every record
// through the otelslog bridge, which stamps it with the trace and span ID.
func newLogger(w io.Writer, t *telemetry) *slog.Logger {
	return slog.New(slog.NewMultiHandler(
		slog.NewJSONHandler(w, nil),
		otelslog.NewHandler("users", otelslog.WithLoggerProvider(t.lp)),
	))
}
