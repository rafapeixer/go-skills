package main

import (
	"io"
	"net"
	"net/http"
	"testing"
)

// TestRunEndToEnd starts the service through run, as main does, and talks
// real HTTP to it, so routing, method patterns and status codes are covered.
func TestRunEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	// t.Context is cancelled before Cleanup runs, which shuts the server down.
	go func() { errc <- run(t.Context(), ln) }()
	t.Cleanup(func() {
		if err := <-errc; err != nil {
			t.Errorf("run() = %v", err)
		}
	})

	tests := []struct {
		name, method, path string
		wantStatus         int
		wantBody           string
	}{
		{"converts", http.MethodGet, "/convert?amount=10.00&currency=EUR", http.StatusOK, "9.20\n"},
		{"rejects bad amount", http.MethodGet, "/convert?amount=ten&currency=EUR", http.StatusBadRequest, ""},
		{"rejects wrong method", http.MethodPost, "/convert", http.StatusMethodNotAllowed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tt.method, "http://"+ln.Addr().String()+tt.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("%s %s: status %d, want %d (body %q)", tt.method, tt.path, resp.StatusCode, tt.wantStatus, body)
			}
			if tt.wantBody != "" && string(body) != tt.wantBody {
				t.Errorf("%s %s: body %q, want %q", tt.method, tt.path, body, tt.wantBody)
			}
		})
	}
}
