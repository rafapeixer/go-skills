package main

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	errBusy        = errors.New("server busy: too many tool calls in flight, retry later")
	errToolTimeout = errors.New("tool call exceeded its time budget")
)

// limiter caps the tool calls one replica runs at once and how long each
// may take. One limiter is shared by every tool on the server.
type limiter struct {
	slots   chan struct{}
	timeout time.Duration
}

func newLimiter(maxInFlight int, timeout time.Duration) *limiter {
	return &limiter{slots: make(chan struct{}, maxInFlight), timeout: timeout}
}

// bounded wraps a typed handler: it fails fast when every slot is taken
// and gives the call a deadline derived from the request context.
func bounded[In, Out any](l *limiter, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		select {
		case l.slots <- struct{}{}:
		default:
			var zero Out
			return nil, zero, errBusy // a tool error: the model sees it and can retry later
		}
		defer func() { <-l.slots }()
		ctx, cancel := context.WithTimeoutCause(ctx, l.timeout, errToolTimeout)
		defer cancel()
		return h(ctx, req, in)
	}
}
