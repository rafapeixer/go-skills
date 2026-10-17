package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

const maxBodyBytes = 1 << 20 // 1 MiB is plenty for this API's JSON

// newHandler builds the mux and wraps it in middleware, outermost last.
func newHandler(logger *slog.Logger, s *store, shuttingDown *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	addRoutes(mux, s, shuttingDown)
	var h http.Handler = mux
	h = http.NewCrossOriginProtection().Handler(h)
	h = logRequests(logger)(h)
	return h
}

// addRoutes is the whole API surface of the service, in one place.
func addRoutes(mux *http.ServeMux, s *store, shuttingDown *atomic.Bool) {
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("GET /readyz", handleReadyz(s, shuttingDown))
	mux.Handle("GET /items/{id}", handleGetItem(s))
	mux.Handle("POST /items", handleCreateItem(s))
}

func logRequests(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			logger.Info("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
		})
	}
}

// handleHealthz answers "is the process alive?" and checks no dependency.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleReadyz answers "should I get traffic?": false while shutting down
// or while a dependency is unreachable.
func handleReadyz(s *store, shuttingDown *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shuttingDown.Load() || s.ping(r.Context()) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func handleGetItem(s *store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := s.get(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": r.PathValue("id"), "name": name})
	})
}

func handleCreateItem(s *store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		var in struct{ ID, Name string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if in.ID == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		s.put(in.ID, in.Name)
		w.WriteHeader(http.StatusCreated)
	})
}
