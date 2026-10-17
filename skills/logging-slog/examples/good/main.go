// Command good builds one logging chain in run: a JSON handler, an optional
// fan-out, and a context handler that adds request_id. Everything else gets
// the *slog.Logger as an argument.
package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
)

func main() {
	if err := run(os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(getenv func(string) string, stdout io.Writer) error {
	var level slog.Level // the zero value is Info
	if s := getenv("LOG_LEVEL"); s != "" {
		if err := level.UnmarshalText([]byte(s)); err != nil {
			return fmt.Errorf("parse LOG_LEVEL %q: %w", s, err)
		}
	}
	logger := newLogger(level, stdout)
	slog.SetDefault(logger) // only here, for dependencies that still call slog.Info

	users := map[string]User{"1": {ID: "1", Email: "ana@example.com", APIKey: "sk_live_123"}}
	h := newHandler(logger, users)

	// A real service serves h from an http.Server (see the http-server skill);
	// one request is enough to show the log lines.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/users/1", nil))
	return nil
}

// newLogger builds the handler chain of the process: the context handler
// first, so every sink gets request_id, then the fan-out, then the sinks.
func newLogger(level slog.Leveler, w io.Writer, extra ...slog.Handler) *slog.Logger {
	var h slog.Handler = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	if len(extra) > 0 {
		h = slog.NewMultiHandler(append([]slog.Handler{h}, extra...)...)
	}
	return slog.New(contextHandler{h})
}
