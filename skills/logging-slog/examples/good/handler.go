package main

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

func newHandler(logger *slog.Logger, users map[string]User) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", handleGetUser(logger, users))
	return logRequests(logger)(mux)
}

func handleGetUser(logger *slog.Logger, users map[string]User) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := users[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// Detail goes to Debug; the context carries the request ID.
		logger.DebugContext(r.Context(), "user loaded", "user", u)
		fmt.Fprintln(w, u.ID)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests puts a request ID in the context and writes one access line
// per request with typed attributes.
func logRequests(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			r = r.WithContext(withRequestID(r.Context(), rand.Text()))
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
				slog.String(keyMethod, r.Method),
				slog.String(keyRoute, r.Pattern), // the route pattern, never the raw URL
				slog.Int(keyStatus, sw.status),
				slog.Int64(keyDurationMS, time.Since(start).Milliseconds()),
			)
		})
	}
}
