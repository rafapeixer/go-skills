package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// BenchmarkInstrumentation prices otelhttp on this handler. Compare ns/op and
// allocs/op of the two cases with benchstat before and after an upgrade:
//
//	go test -run '^$' -bench Instrumentation -benchmem -count 10
func BenchmarkInstrumentation(b *testing.B) {
	tel := newTelemetry(resource.Empty(),
		sdktrace.NewBatchSpanProcessor(tracetest.NewNoopExporter()),
		sdkmetric.NewManualReader(), sdklog.NewSimpleProcessor(&logRecorder{}), 1)
	b.Cleanup(func() { tel.Shutdown(b.Context()) })
	logger := newLogger(io.Discard, tel)

	bare := http.NewServeMux()
	bare.Handle("GET /users/{id}", handleGetUser(logger))
	cases := []struct {
		name string
		h    http.Handler
	}{
		{"bare", bare},
		{"otelhttp", newHandler(tel, propagation.TraceContext{}, logger)},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			req := httptest.NewRequest("GET", "/users/42", nil)
			for b.Loop() {
				c.h.ServeHTTP(httptest.NewRecorder(), req)
			}
		})
	}
}
