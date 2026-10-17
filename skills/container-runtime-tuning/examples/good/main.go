// Command server is a minimal HTTP service tuned for a container: it leaves
// GOMAXPROCS and GOGC to the runtime, derives GOMEMLIMIT from the container
// memory limit, and exports GC CPU and heap goal from runtime/metrics.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"
	_ "time/tzdata" // embeds the zone database (~450 KB) so the binary does not depend on the image
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	memLimit, err := applyMemoryLimit(os.Getenv)
	if err != nil {
		return err
	}
	if os.Getenv("GOMAXPROCS") != "" {
		slog.Warn("GOMAXPROCS is set; the runtime will not follow CPU limit changes")
	}
	// GOMAXPROCS is the value now; the runtime updates it if the CPU limit changes.
	slog.Info("runtime settings",
		"gomaxprocs", runtime.GOMAXPROCS(0),
		"numcpu", runtime.NumCPU(),
		"gomemlimit_bytes", memLimit,
		"go", runtime.Version(),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/runtime", serveRuntimeMetrics)
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return srv.ListenAndServe()
}
