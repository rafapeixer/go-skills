package main

import (
	"net/http/httptest"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"testing"
)

// TestRuntimeMetricsSupported fails when a Go release renames or drops a
// metric the dashboards depend on; metrics.Read would silently return KindBad.
func TestRuntimeMetricsSupported(t *testing.T) {
	supported := make(map[string]metrics.ValueKind)
	for _, d := range metrics.All() {
		supported[d.Name] = d.Kind
	}
	for name, want := range runtimeMetrics {
		got, ok := supported[name]
		if !ok {
			t.Errorf("metric %s is not supported by this Go release", name)
			continue
		}
		if got != want {
			t.Errorf("metric %s has kind %v, want %v", name, got, want)
		}
	}
}

func TestServeRuntimeMetrics(t *testing.T) {
	rec := httptest.NewRecorder()
	serveRuntimeMetrics(rec, httptest.NewRequest("GET", "/debug/runtime", nil))
	body := rec.Body.String()
	for name := range runtimeMetrics {
		if !strings.Contains(body, name+" ") {
			t.Errorf("response is missing %s:\n%s", name, body)
		}
	}
}

func TestApplyMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })

	tests := []struct {
		name    string
		env     map[string]string
		want    int64
		wantErr bool
	}{
		{"90% of the container limit", map[string]string{"MEMORY_LIMIT_BYTES": "1073741824"}, 966367638, false},
		{"explicit GOMEMLIMIT wins", map[string]string{"GOMEMLIMIT": "1GiB", "MEMORY_LIMIT_BYTES": "1"}, prev, false},
		{"invalid limit", map[string]string{"MEMORY_LIMIT_BYTES": "512Mi"}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			debug.SetMemoryLimit(prev)
			got, err := applyMemoryLimit(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("applyMemoryLimit() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("applyMemoryLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}
