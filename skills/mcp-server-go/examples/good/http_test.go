package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenRequired(t *testing.T) {
	ts := httptest.NewServer(newHandler(newServer(storeFunc(nil), newLimiter(1, time.Second)), verifier(testTokens)))
	defer ts.Close()
	for _, token := range []string{"", "other-aud", "forged"} {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", ts.URL+"/mcp", strings.NewReader(`{}`))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: status %d, want 401", token, resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
			t.Errorf("token %q: WWW-Authenticate %q lacks resource_metadata", token, resp.Header.Get("WWW-Authenticate"))
		}
	}
}

// roundRobin plays a load balancer without session affinity.
type roundRobin struct {
	next     atomic.Uint64
	replicas []http.Handler
}

func (rr *roundRobin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rr.replicas[rr.next.Add(1)%uint64(len(rr.replicas))].ServeHTTP(w, r)
}

func TestAnyReplicaServesAnyRequest(t *testing.T) {
	var calls [2]atomic.Int32
	lb := &roundRobin{}
	for i := range calls {
		db := storeFunc(func(context.Context, string, string, int) ([]Order, error) {
			calls[i].Add(1)
			return nil, nil
		})
		lb.replicas = append(lb.replicas, newHandler(newServer(db, newLimiter(4, time.Second)), verifier(testTokens)))
	}
	cs := connect(t, lb)
	for range 4 {
		res, err := search(t.Context(), cs, map[string]any{"status": "open"})
		if err != nil || res.IsError {
			t.Fatalf("call through the balancer = %v, %q", err, text(res))
		}
	}
	if calls[0].Load() == 0 || calls[1].Load() == 0 {
		t.Errorf("calls per replica = %d, %d; want both replicas used", calls[0].Load(), calls[1].Load())
	}
}
