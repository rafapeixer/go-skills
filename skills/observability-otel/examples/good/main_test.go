package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const sampledParent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

// logRecorder is an in-memory log exporter: it keeps each record's trace ID.
type logRecorder struct {
	mu       sync.Mutex
	traceIDs []trace.TraceID
}

func (l *logRecorder) Export(_ context.Context, rs []sdklog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range rs {
		l.traceIDs = append(l.traceIDs, r.TraceID())
	}
	return nil
}
func (l *logRecorder) Shutdown(context.Context) error   { return nil }
func (l *logRecorder) ForceFlush(context.Context) error { return nil }

type fixture struct {
	tel     *telemetry
	spans   *tracetest.InMemoryExporter
	metrics *sdkmetric.ManualReader
	logs    *logRecorder
	handler http.Handler
}

// newFixture uses synchronous processors, so every span and log record is
// exported before ServeHTTP returns: no goroutines, no waiting, no races.
func newFixture(t *testing.T, ratio float64) *fixture {
	f := &fixture{spans: tracetest.NewInMemoryExporter(), metrics: sdkmetric.NewManualReader(), logs: &logRecorder{}}
	f.tel = newTelemetry(resource.Empty(), sdktrace.NewSimpleSpanProcessor(f.spans), f.metrics, sdklog.NewSimpleProcessor(f.logs), ratio)
	t.Cleanup(func() { f.tel.Shutdown(context.Background()) })
	f.handler = newHandler(f.tel, propagation.TraceContext{}, newLogger(io.Discard, f.tel))
	return f
}

func (f *fixture) get(path, traceparent string) {
	req := httptest.NewRequest("GET", path, nil)
	if traceparent != "" {
		req.Header.Set("traceparent", traceparent)
	}
	f.handler.ServeHTTP(httptest.NewRecorder(), req)
}
