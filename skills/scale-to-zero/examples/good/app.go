package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

const connectTimeout = 3 * time.Second

// app holds the dependencies. Each one is a function that builds it on the
// first call and returns the same value (or the same error) afterwards.
type app struct {
	store  func() (*store, error)
	report func() (*template.Template, error)
	opened atomic.Pointer[store] // set once the store exists, for close
}

func newApp(dsn string, open func(string) (*store, error)) *app {
	a := &app{report: sync.OnceValues(parseReport)}
	a.store = sync.OnceValues(func() (*store, error) {
		s, err := open(dsn)
		if err == nil {
			a.opened.Store(s)
		}
		return s, err
	})
	return a
}

// close releases only what was built; calling a.store() here would open the
// store just to close it.
func (a *app) close() {
	if s := a.opened.Load(); s != nil {
		s.close()
	}
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /items/{id}", a.handleItem)
	mux.HandleFunc("GET /report", a.handleReport)
	return mux
}

func (a *app) handleItem(w http.ResponseWriter, r *http.Request) {
	s, err := a.store()
	var v string
	if err == nil {
		v, err = s.item(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, v)
}

func (a *app) handleReport(w http.ResponseWriter, r *http.Request) {
	t, err := a.report()
	if err != nil {
		http.Error(w, "report unavailable", http.StatusInternalServerError)
		return
	}
	_ = t.Execute(w, time.Now().UTC()) // headers are sent; a failure means the client left
}

func parseReport() (*template.Template, error) {
	return template.New("report").Parse(`<h1>Report at {{.Format "15:04"}}</h1>`)
}

// store stands in for pgxpool.NewWithConfig with MinConns 0: opening it only
// validates configuration and does not dial, so its error is permanent and
// safe to cache in sync.OnceValues.
type store struct{ dsn *url.URL }

func openStore(dsn string) (*store, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if u.Host == "" {
		return nil, errors.New("parse DATABASE_URL: no host")
	}
	return &store{dsn: u}, nil
}

// item is where a real pool dials, on first use, bounded by the request
// deadline and connectTimeout. The example stays offline.
func (s *store) item(ctx context.Context, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	return "item " + id, ctx.Err()
}

func (s *store) close() {}
