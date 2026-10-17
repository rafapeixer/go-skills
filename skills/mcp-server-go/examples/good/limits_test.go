package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestToolTimeoutIsToolError(t *testing.T) {
	db := storeFunc(func(ctx context.Context, _, _ string, _ int) ([]Order, error) {
		<-ctx.Done() // a slow query that honors ctx
		return nil, ctx.Err()
	})
	res, err := search(t.Context(), serve(t, db, newLimiter(4, 50*time.Millisecond)), map[string]any{"status": "open"})
	if err != nil {
		t.Fatalf("call err = %v, want a tool result", err)
	}
	if !res.IsError || !strings.Contains(text(res), errToolTimeout.Error()) {
		t.Errorf("result = %q (isError %v), want %q", text(res), res.IsError, errToolTimeout)
	}
}

func TestConcurrencyLimitFailsFast(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	db := storeFunc(func(context.Context, string, string, int) ([]Order, error) {
		close(entered)
		<-release
		return nil, nil
	})
	cs := serve(t, db, newLimiter(1, time.Minute))

	first := make(chan error, 1)
	go func() {
		_, err := search(t.Context(), cs, map[string]any{"status": "open"})
		first <- err
	}()
	<-entered // the only slot is now taken

	res, err := search(t.Context(), cs, map[string]any{"status": "open"})
	if err != nil || !res.IsError || text(res) != errBusy.Error() {
		t.Errorf("second call = %v, %q, want tool error %q", err, text(res), errBusy)
	}
	close(release)
	if err := <-first; err != nil {
		t.Errorf("first call: %v", err)
	}
}
