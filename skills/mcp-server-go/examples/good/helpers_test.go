package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// storeFunc lets each test script the downstream dependency.
type storeFunc func(ctx context.Context, owner, status string, limit int) ([]Order, error)

func (f storeFunc) Search(ctx context.Context, owner, status string, limit int) ([]Order, error) {
	return f(ctx, owner, status, limit)
}

var testTokens = map[string]grant{
	"alice-token": {user: "alice", audience: resource, scopes: []string{scope}, expires: time.Now().Add(time.Hour)},
	"other-aud":   {user: "alice", audience: "https://other.example.com/mcp", scopes: []string{scope}, expires: time.Now().Add(time.Hour)},
}

// bearer adds the token to every request, as an OAuth client would.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// connect starts the real HTTP handler and returns an SDK client session.
func connect(t *testing.T, h http.Handler) *mcp.ClientSession {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{"alice-token"}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func serve(t *testing.T, db store, lim *limiter) *mcp.ClientSession {
	return connect(t, newHandler(newServer(db, lim), verifier(testTokens)))
}

func search(ctx context.Context, cs *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, error) {
	return cs.CallTool(ctx, &mcp.CallToolParams{Name: "search_orders", Arguments: args})
}

func text(res *mcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}
