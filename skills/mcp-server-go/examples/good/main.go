// Command good exposes one narrow, read-only tool over MCP: typed input and
// output structs, a schema with an allow-list, a per-call deadline and
// concurrency cap, stateless streamable HTTP and OAuth bearer tokens.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const (
	resource = "https://orders.example.com/mcp" // canonical URI; tokens must name it as audience
	issuer   = "https://auth.example.com"
	scope    = "orders:read"
)

// newHandler serves MCP statelessly, so any replica behind the load balancer
// can answer any request, and rejects requests without a valid bearer token.
func newHandler(srv *mcp.Server, verify auth.TokenVerifier) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true})
	metadataURL := "https://orders.example.com/.well-known/oauth-protected-resource"
	requireToken := auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: metadataURL,
		Scopes:              []string{scope},
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", requireToken(mcpHandler))
	mux.Handle("/.well-known/oauth-protected-resource", auth.ProtectedResourceMetadataHandler(
		&oauthex.ProtectedResourceMetadata{
			Resource:             resource,
			AuthorizationServers: []string{issuer},
			ScopesSupported:      []string{scope},
		}))
	return mux
}

// grant is what a verified token says. A real verifier checks a JWT's
// signature against the issuer's keys (or introspects the token) and then
// applies the same checks as below.
type grant struct {
	user, audience string
	scopes         []string
	expires        time.Time
}

func verifier(tokens map[string]grant) auth.TokenVerifier {
	return func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		g, ok := tokens[token]
		if !ok || g.audience != resource { // reject tokens issued for another server
			return nil, fmt.Errorf("unknown token or wrong audience: %w", auth.ErrInvalidToken)
		}
		return &auth.TokenInfo{UserID: g.user, Scopes: g.scopes, Expiration: g.expires}, nil
	}
}

type memStore map[string][]Order // orders by owner

func (m memStore) Search(ctx context.Context, owner, status string, limit int) ([]Order, error) {
	var out []Order
	for _, o := range m[owner] {
		if o.Status == status && len(out) < limit {
			out = append(out, o)
		}
	}
	return out, ctx.Err()
}

func main() {
	srv := newServer(memStore{"alice": {{ID: "o-1", Status: "open", TotalCents: 1250}}}, newLimiter(32, 5*time.Second))
	httpSrv := &http.Server{
		Addr:              "localhost:8080",
		Handler:           newHandler(srv, verifier(nil)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(httpSrv.ListenAndServe())
}
