package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serveOnce(h http.Handler, method, target, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutes(t *testing.T) {
	var shuttingDown atomic.Bool
	s := newStore()
	s.put("1", "pen")
	h := newHandler(slog.New(slog.DiscardHandler), s, &shuttingDown)

	tests := []struct {
		name, method, target, body string
		header                     map[string]string
		want                       int
	}{
		{"path value", "GET", "/items/1", "", nil, http.StatusOK},
		{"missing item", "GET", "/items/2", "", nil, http.StatusNotFound},
		{"wrong method", "DELETE", "/items/1", "", nil, http.StatusMethodNotAllowed},
		{"create", "POST", "/items", `{"id":"3","name":"cup"}`, nil, http.StatusCreated},
		{"body over limit", "POST", "/items", `{"name":"` + strings.Repeat("a", 2*maxBodyBytes) + `"}`, nil, http.StatusRequestEntityTooLarge},
		{"cross-site POST", "POST", "/items", `{"id":"4"}`, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveOnce(h, tt.method, tt.target, tt.body, tt.header)
			if rec.Code != tt.want {
				t.Fatalf("%s %s = %d, want %d", tt.method, tt.target, rec.Code, tt.want)
			}
			if rec.Code == http.StatusMethodNotAllowed && rec.Header().Get("Allow") == "" {
				t.Error("405 without an Allow header")
			}
		})
	}
}

func TestReadyzFailsWhileShuttingDown(t *testing.T) {
	var shuttingDown atomic.Bool
	h := newHandler(slog.New(slog.DiscardHandler), newStore(), &shuttingDown)
	shuttingDown.Store(true)

	if rec := serveOnce(h, "GET", "/readyz", "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d during shutdown, want 503", rec.Code)
	}
	if rec := serveOnce(h, "GET", "/healthz", "", nil); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d during shutdown, want 200", rec.Code)
	}
}

func TestServeDrainsInFlightRequests(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	srv := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond) // a slow request still running at SIGTERM
		w.WriteHeader(http.StatusOK)
	}))

	ctx, cancel := context.WithCancel(t.Context())
	var shuttingDown atomic.Bool
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, &shuttingDown, 10*time.Millisecond) }()

	status := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			status <- 0
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()

	<-started
	cancel() // what SIGTERM does through signal.NotifyContext
	if got := <-status; got != http.StatusOK {
		t.Errorf("in-flight request got %d, want 200; shutdown cut it off", got)
	}
	if err := <-done; err != nil {
		t.Errorf("serve returned %v, want nil after a clean shutdown", err)
	}
	if !shuttingDown.Load() {
		t.Error("readiness was not failed before Shutdown")
	}
}
