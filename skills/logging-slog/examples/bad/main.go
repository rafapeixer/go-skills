// Command bad compiles and logs, but every marked line is a mistake that
// leaks secrets, loses correlation or breaks log queries.
package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/pprof"
)

type User struct {
	ID     string
	Email  string
	APIKey string
}

type store struct{}

func (store) user(ctx context.Context, id string) User {
	// MISTAKE: a library package reads the global logger, so tests cannot
	// capture or silence it, and parallel tests that swap it race.
	slog.Default().Info("loading user", "userId", id)
	return User{ID: id, Email: "ana@example.com", APIKey: "sk_live_123"}
}

func handleGetUser(s store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// MISTAKE: logs every header, including Authorization and Cookie,
		// so every bearer token and session cookie ends up in the log store.
		slog.Info("request", "headers", r.Header, "url", r.URL.String())

		// MISTAKE: the token becomes a pprof label; labels show up in CPU and
		// goroutine profiles, and in tracebacks for go 1.27 modules.
		pprof.Do(r.Context(), pprof.Labels("token", r.Header.Get("Authorization")), func(ctx context.Context) {
			u := s.user(ctx, r.PathValue("id"))
			// MISTAKE: no ctx, so a context handler cannot add the request or
			// trace ID; and the whole struct is logged, email and API key included.
			slog.Info("user loaded", "user", u)
			// MISTAKE: a third spelling of the same key; a query on user_id
			// misses "userId" and "user-id".
			slog.Info("responding", "user-id", u.ID)
		})

		// MISTAKE: sampling at the call site with math/rand drops 90% of
		// failures too, and every call site samples differently.
		if rand.IntN(10) == 0 {
			slog.Error("slow response", "path", r.URL.Path)
		}
	}
}

func main() {
	// MISTAKE: TextHandler in production; key=value lines with free-form
	// values are not reliably parseable by the log pipeline.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", handleGetUser(store{}))
	req := httptest.NewRequest("GET", "/users/1", nil)
	req.Header.Set("Authorization", "Bearer sk_live_123")
	mux.ServeHTTP(httptest.NewRecorder(), req)
}
