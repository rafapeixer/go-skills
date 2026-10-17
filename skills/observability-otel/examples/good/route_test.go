package main

import (
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestSpansAndMetricsUseRoutePattern(t *testing.T) {
	f := newFixture(t, 1)
	f.get("/users/42", "")
	f.get("/users/43", "")

	spans := f.spans.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	for _, s := range spans {
		set := attribute.NewSet(s.Attributes...)
		route, _ := set.Value("http.route")
		if s.Name != "GET /users/{id}" || route.AsString() != "/users/{id}" {
			t.Errorf("span %q with http.route %q, want the route pattern", s.Name, route.AsString())
		}
	}

	var rm metricdata.ResourceMetrics
	if err := f.metrics.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if strings.HasSuffix(m.Name, ".body.size") {
				t.Errorf("%s was exported; the view should drop it", m.Name)
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if m.Name != "http.server.request.duration" || !ok {
				continue
			}
			found = true
			if len(h.DataPoints) != 1 {
				t.Errorf("%d duration series for two user IDs, want 1", len(h.DataPoints))
			}
			for _, kv := range h.DataPoints[0].Attributes.ToSlice() {
				if strings.Contains(kv.Value.String(), "42") || kv.Key == "server.address" {
					t.Errorf("unbounded metric attribute %s=%s", kv.Key, kv.Value.String())
				}
			}
		}
	}
	if !found {
		t.Fatal("no http.server.request.duration histogram")
	}
}
