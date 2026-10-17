package main

import (
	"context"
	"log/slog"
)

// The key vocabulary of this service. Every log line uses these names, so a
// query for request_id or duration_ms finds every line that has one.
const (
	keyRequestID  = "request_id"
	keyMethod     = "method"
	keyRoute      = "route"
	keyStatus     = "status"
	keyDurationMS = "duration_ms"
)

type requestIDKey struct{}

// withRequestID returns a context that carries id for log correlation.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// contextHandler adds request_id from the context to every record, so call
// sites only pass ctx and never thread the ID by hand.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		r.AddAttrs(slog.String(keyRequestID, id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
