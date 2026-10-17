package main

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"runtime/metrics"
	"sync/atomic"
)

// newDebugMux registers the pprof handlers explicitly on a mux that only the
// internal listener serves. pprof.Index also serves the named profiles
// (heap, allocs, goroutine, mutex, block) under /debug/pprof/.
func newDebugMux(stats *efficiency) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("GET /debug/efficiency", stats.serve)
	return mux
}

// efficiency exports the two counters behind bytes allocated per request:
// divide rate(heap_alloc_bytes_total) by rate(requests_total) on the
// dashboard. CPU-seconds per request divides the container CPU counter by
// the same request rate.
type efficiency struct {
	requests atomic.Uint64
}

func (e *efficiency) count(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.requests.Add(1)
		next.ServeHTTP(w, r)
	})
}

const allocBytesMetric = "/gc/heap/allocs:bytes"

func (e *efficiency) serve(w http.ResponseWriter, _ *http.Request) {
	s := []metrics.Sample{{Name: allocBytesMetric}}
	metrics.Read(s)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "requests_total %d\n", e.requests.Load())
	fmt.Fprintf(w, "heap_alloc_bytes_total %d\n", s[0].Value.Uint64())
}
