package upstream

import (
	"compress/gzip"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newCountingServer starts h and counts every TCP connection the client opens.
func newCountingServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &conns
}

func TestSequentialCallsReuseOneConnection(t *testing.T) {
	var n atomic.Int64
	srv, conns := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 0 {
			// Error responses with a body: the client returns early on these.
			http.Error(w, strings.Repeat("unavailable ", 100), http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(strings.Repeat("ok ", 1000)))
	})
	c, err := New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	for i := range 50 {
		_, err := c.Get(t.Context(), "/items")
		if wantErr := i%2 == 1; (err != nil) != wantErr {
			t.Fatalf("call %d: err = %v, want error %v", i, err, wantErr)
		}
	}
	if got := conns.Load(); got != 1 {
		t.Errorf("50 sequential calls opened %d connections, want 1", got)
	}
}

func TestGzipIsDecodedTransparently(t *testing.T) {
	srv, _ := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding = %q, want the Transport's gzip", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		zw.Write([]byte("hello"))
		zw.Close()
	})
	c, err := New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	body, err := c.Get(t.Context(), "/greeting")
	if err != nil || string(body) != "hello" {
		t.Errorf("Get = %q, %v; want decoded %q", body, err, "hello")
	}
}
