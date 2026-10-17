package main

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// telemetry owns the three SDK providers. run builds it once, passes it to
// the code that instruments, and shuts it down last.
type telemetry struct {
	tp *sdktrace.TracerProvider
	mp *sdkmetric.MeterProvider
	lp *sdklog.LoggerProvider
}

// newTelemetry takes the processors and reader as arguments, so run passes
// OTLP exporters and tests pass in-memory ones.
func newTelemetry(res *resource.Resource, spans sdktrace.SpanProcessor, metrics sdkmetric.Reader, logs sdklog.Processor, ratio float64) *telemetry {
	return &telemetry{
		tp: sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSpanProcessor(spans),
			// Follow the caller's decision; sample only a share of new traces.
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		),
		mp: sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(metrics),
			sdkmetric.WithView(views()...),
		),
		lp: sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(logs)),
	}
}

// views drop what nobody queries before it is exported and billed.
func views() []sdkmetric.View {
	drop := sdkmetric.Stream{Aggregation: sdkmetric.AggregationDrop{}}
	return []sdkmetric.View{
		sdkmetric.NewView(sdkmetric.Instrument{Name: "http.server.request.body.size"}, drop),
		sdkmetric.NewView(sdkmetric.Instrument{Name: "http.server.response.body.size"}, drop),
		// server.address comes from the client's Host header: unbounded.
		sdkmetric.NewView(sdkmetric.Instrument{Name: "http.server.request.duration"},
			sdkmetric.Stream{AttributeFilter: attribute.NewAllowKeysFilter(
				semconv.HTTPRequestMethodKey, semconv.HTTPRouteKey,
				semconv.HTTPResponseStatusCodeKey, semconv.ErrorTypeKey)}),
	}
}

// Shutdown flushes buffered spans, metrics and logs, then stops the
// exporters. Without it the last batch before exit is lost.
func (t *telemetry) Shutdown(ctx context.Context) error {
	return errors.Join(t.tp.Shutdown(ctx), t.mp.Shutdown(ctx), t.lp.Shutdown(ctx))
}
