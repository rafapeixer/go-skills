package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestSamplingFollowsParent(t *testing.T) {
	tests := []struct {
		name, traceparent string
		wantSpans         int
	}{
		{"new trace, ratio 0", "", 0},
		{"caller sampled", sampledParent, 1},
		{"caller did not sample", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, 0) // ratio 0: only a sampled parent can record
			f.get("/users/42", tt.traceparent)
			if got := len(f.spans.GetSpans()); got != tt.wantSpans {
				t.Fatalf("got %d spans, want %d", got, tt.wantSpans)
			}
		})
	}
}

func TestLogsCarryTraceID(t *testing.T) {
	f := newFixture(t, 0)
	f.get("/users/42", sampledParent)

	want := f.spans.GetSpans()[0].SpanContext.TraceID()
	f.logs.mu.Lock()
	defer f.logs.mu.Unlock()
	if len(f.logs.traceIDs) != 1 || f.logs.traceIDs[0] != want {
		t.Fatalf("log trace IDs = %v, want [%s]", f.logs.traceIDs, want)
	}
}

func TestClientPropagatesTraceContext(t *testing.T) {
	f := newFixture(t, 1)
	got := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("traceparent")
	}))
	defer upstream.Close()

	ctx, span := f.tel.tp.Tracer("test").Start(t.Context(), "parent")
	req, _ := http.NewRequestWithContext(ctx, "GET", upstream.URL+"/orders/7", nil)
	resp, err := newClient(f.tel, propagation.TraceContext{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	span.End()

	sc := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(t.Context(),
		propagation.HeaderCarrier{"Traceparent": {<-got}}))
	if sc.TraceID() != span.SpanContext().TraceID() || !sc.IsSampled() {
		t.Errorf("upstream got trace %s sampled=%v, want %s sampled", sc.TraceID(), sc.IsSampled(), span.SpanContext().TraceID())
	}
}
