package main

import (
	"net/http"
	"net/http/httptest"
	"runtime/metrics"
	"strings"
	"testing"
)

func status(h http.Handler, target string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code
}

func TestPublicMuxDoesNotExposePprof(t *testing.T) {
	public := newPublicMux()
	for _, target := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/cmdline", "/debug/efficiency"} {
		if got := status(public, target); got != http.StatusNotFound {
			t.Errorf("public GET %s = %d, want 404", target, got)
		}
	}
	if got := status(public, "/report?rows=3"); got != http.StatusOK {
		t.Errorf("public GET /report = %d, want 200", got)
	}
}

// The import alone registers pprof on http.DefaultServeMux, which is why no
// server in this program may use a nil Handler.
func TestImportRegistersOnDefaultServeMux(t *testing.T) {
	_, pattern := http.DefaultServeMux.Handler(httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if pattern == "" {
		t.Fatal("expected net/http/pprof to register on DefaultServeMux")
	}
}

func TestDebugMuxServesProfiles(t *testing.T) {
	debug := newDebugMux(new(efficiency))
	for _, target := range []string{"/debug/pprof/", "/debug/pprof/heap?debug=1", "/debug/pprof/goroutine?debug=1", "/debug/efficiency"} {
		if got := status(debug, target); got != http.StatusOK {
			t.Errorf("debug GET %s = %d, want 200", target, got)
		}
	}
}

func TestEfficiencyCountsRequests(t *testing.T) {
	var stats efficiency
	h := stats.count(newPublicMux())
	for range 3 {
		status(h, "/report?rows=1")
	}
	rec := httptest.NewRecorder()
	stats.serve(rec, httptest.NewRequest(http.MethodGet, "/debug/efficiency", nil))
	if body := rec.Body.String(); !strings.Contains(body, "requests_total 3\n") {
		t.Errorf("efficiency body = %q, want requests_total 3", body)
	}
}

func TestAllocMetricSupported(t *testing.T) {
	for _, d := range metrics.All() {
		if d.Name == allocBytesMetric && d.Kind == metrics.KindUint64 {
			return
		}
	}
	t.Fatalf("metric %s is not supported by this Go release", allocBytesMetric)
}
