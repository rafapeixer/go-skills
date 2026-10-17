package main

import (
	"cmp"
	"context"
	"errors"
	"log/slog"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Order struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	TotalCents int    `json:"total_cents"`
}

// store is the downstream dependency. It takes the caller's identity from
// the token, never from tool arguments, so a model cannot ask for another
// customer's orders.
type store interface {
	Search(ctx context.Context, owner, status string, limit int) ([]Order, error)
}

// searchIn is the tool's input. The SDK infers the JSON schema from it and
// validates every call against that schema before the handler runs.
type searchIn struct {
	Status string `json:"status" jsonschema:"order status to filter by"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum number of orders to return (1 to 50, default 10)"`
}

type searchOut struct {
	Orders []Order `json:"orders"`
}

var (
	errNoCaller    = errors.New("no authenticated caller")
	errUnavailable = errors.New("order store unavailable, retry later")
)

// searchSchema tightens the inferred schema with what Go types cannot say:
// an allow-list for status and a range for limit.
func searchSchema() *jsonschema.Schema {
	s, err := jsonschema.For[searchIn](nil)
	if err != nil {
		panic(err)
	}
	s.Properties["status"].Enum = []any{"open", "shipped", "cancelled"}
	s.Properties["limit"].Minimum = new(1.0)
	s.Properties["limit"].Maximum = new(50.0)
	return s
}

func searchOrders(db store) mcp.ToolHandlerFor[searchIn, searchOut] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		if req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
			return nil, searchOut{}, errNoCaller
		}
		// ctx is cancelled when the client cancels, disconnects or the
		// per-call budget runs out; the store query stops with it.
		orders, err := db.Search(ctx, req.Extra.TokenInfo.UserID, in.Status, cmp.Or(in.Limit, 10))
		if cause := context.Cause(ctx); cause != nil {
			return nil, searchOut{}, cause
		}
		if err != nil {
			// Log the details; give the model a message it can act on.
			slog.ErrorContext(ctx, "search orders", "err", err)
			return nil, searchOut{}, errUnavailable
		}
		return nil, searchOut{Orders: orders}, nil
	}
}

func newServer(db store, lim *limiter) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "orders", Version: "v1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_orders",
		Description: "List the caller's orders with a given status.",
		InputSchema: searchSchema(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, bounded(lim, searchOrders(db)))
	return srv
}
