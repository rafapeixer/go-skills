package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// MISTAKE: this CPU profile is later copied to default.pgo with
// `go test -bench . -cpuprofile default.pgo`. A microbenchmark exercises one
// function, so PGO optimizes for a workload production never runs.
func BenchmarkReport(b *testing.B) {
	// MISTAKE: setup inside a b.N benchmark is timed unless b.ResetTimer is
	// called; b.Loop would run it once and time only the loop.
	req := httptest.NewRequest("GET", "/report?rows=100", nil)
	// MISTAKE: no b.ReportAllocs and no -benchmem, so the pool "optimization"
	// is judged on ns/op alone from a single run, not ten runs and benchstat.
	for i := 0; i < b.N; i++ {
		handleReport(httptest.NewRecorder(), req)
	}
}

func BenchmarkRowLabel(b *testing.B) {
	// MISTAKE: the result is discarded, so the compiler may drop the call and
	// this measures an empty loop. b.Loop keeps the call alive.
	for i := 0; i < b.N; i++ {
		strings.Repeat("row ", 3)
	}
}
