package main

import (
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
)

// applyMemoryLimit sets the soft memory limit to 90% of the container memory
// limit, which the Downward API exposes in MEMORY_LIMIT_BYTES. The other 10% is
// headroom for memory the runtime does not track. An explicit GOMEMLIMIT wins.
// It returns the effective limit.
func applyMemoryLimit(getenv func(string) string) (int64, error) {
	raw := getenv("MEMORY_LIMIT_BYTES")
	if getenv("GOMEMLIMIT") != "" || raw == "" {
		return debug.SetMemoryLimit(-1), nil // -1 reads the limit without changing it
	}
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("parse MEMORY_LIMIT_BYTES %q: want a positive byte count", raw)
	}
	debug.SetMemoryLimit(limit / 10 * 9)
	return debug.SetMemoryLimit(-1), nil
}

// runtimeMetrics are the runtime/metrics samples this service exports. The two
// CPU counters are cumulative: alert on rate(gc) / rate(total), the share of
// CPU the GC takes, which climbs toward 50% when the live heap nears GOMEMLIMIT.
var runtimeMetrics = map[string]metrics.ValueKind{
	"/cpu/classes/gc/total:cpu-seconds": metrics.KindFloat64,
	"/cpu/classes/total:cpu-seconds":    metrics.KindFloat64,
	"/gc/heap/goal:bytes":               metrics.KindUint64,
	"/gc/gomemlimit:bytes":              metrics.KindUint64,
	"/gc/limiter/last-enabled:gc-cycle": metrics.KindUint64,
	"/sched/gomaxprocs:threads":         metrics.KindUint64,
}

// serveRuntimeMetrics writes one "name value" line per metric.
func serveRuntimeMetrics(w http.ResponseWriter, _ *http.Request) {
	samples := make([]metrics.Sample, 0, len(runtimeMetrics))
	for name := range runtimeMetrics {
		samples = append(samples, metrics.Sample{Name: name})
	}
	metrics.Read(samples)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, s := range samples {
		writeSample(w, s)
	}
}

func writeSample(w io.Writer, s metrics.Sample) {
	switch s.Value.Kind() {
	case metrics.KindFloat64:
		fmt.Fprintf(w, "%s %g\n", s.Name, s.Value.Float64())
	case metrics.KindUint64:
		fmt.Fprintf(w, "%s %d\n", s.Name, s.Value.Uint64())
	}
}
