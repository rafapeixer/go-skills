package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/trace"
	"sync"
	"time"
)

// recorder keeps the last seconds of execution trace in memory and writes
// them to dir when a request misses the latency objective.
type recorder struct {
	fr       *trace.FlightRecorder
	dir      string
	slo      time.Duration
	cooldown time.Duration

	mu   sync.Mutex // held while a snapshot is written
	last time.Time
}

func startRecorder(dir string, slo, cooldown time.Duration) (*recorder, error) {
	fr := trace.NewFlightRecorder(trace.FlightRecorderConfig{
		MinAge:   2 * slo,  // keep about twice the window you want to look at
		MaxBytes: 16 << 20, // a busy service produces about 10 MB of trace per second
	})
	if err := fr.Start(); err != nil {
		return nil, fmt.Errorf("start flight recorder: %w", err)
	}
	return &recorder{fr: fr, dir: dir, slo: slo, cooldown: cooldown}, nil
}

func (r *recorder) stop() { r.fr.Stop() }

// watch snapshots the trace in the background when a request is slower than slo.
func (r *recorder) watch(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, req)
		if time.Since(start) > r.slo {
			route := req.URL.Path
			go func() {
				if path, err := r.snapshot(); err != nil {
					slog.Error("flight recorder snapshot", "err", err)
				} else if path != "" {
					slog.Warn("slow request traced", "path", route, "trace", path)
				}
			}()
		}
	})
}

// snapshot writes one trace file and returns its path. It returns "" when
// another snapshot is running or the last one is younger than cooldown, so
// a burst of slow requests produces one file, not thousands.
func (r *recorder) snapshot() (path string, err error) {
	if !r.mu.TryLock() {
		return "", nil
	}
	defer r.mu.Unlock()
	if !r.last.IsZero() && time.Since(r.last) < r.cooldown {
		return "", nil
	}
	r.last = time.Now()

	path = filepath.Join(r.dir, "slow-"+r.last.UTC().Format("20060102T150405.000")+".trace")
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create trace snapshot: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := r.fr.WriteTo(f); err != nil {
		return "", fmt.Errorf("write trace snapshot %s: %w", path, err)
	}
	return path, nil
}
