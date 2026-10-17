package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestSchemaFromInputStruct(t *testing.T) {
	cs := serve(t, storeFunc(nil), newLimiter(4, time.Second))
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("ListTools = %v, %v", tools, err)
	}
	raw, _ := json.Marshal(tools.Tools[0].InputSchema)
	var schema struct {
		Required             []string `json:"required"`
		AdditionalProperties *bool    `json:"additionalProperties"`
		Properties           map[string]struct {
			Enum    []string `json:"enum"`
			Maximum *float64 `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(schema.Required, []string{"status"}) {
		t.Errorf("required = %v, want [status] (limit is omitempty)", schema.Required)
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Errorf("additionalProperties = %v, want false", schema.AdditionalProperties)
	}
	if got := schema.Properties["status"].Enum; len(got) != 3 {
		t.Errorf("status enum = %v, want the 3 allowed statuses", got)
	}
	if m := schema.Properties["limit"].Maximum; m == nil || *m != 50 {
		t.Errorf("limit maximum = %v, want 50", m)
	}
}

func TestScopedToTokenOwner(t *testing.T) {
	db := storeFunc(func(_ context.Context, owner, status string, limit int) ([]Order, error) {
		if owner != "alice" || status != "open" || limit != 10 {
			t.Errorf("Search(%q, %q, %d), want (alice, open, 10)", owner, status, limit)
		}
		return []Order{{ID: "o-1", Status: "open"}}, nil
	})
	res, err := search(t.Context(), serve(t, db, newLimiter(4, time.Second)), map[string]any{"status": "open"})
	if err != nil || res.IsError {
		t.Fatalf("call = %v, %q", err, text(res))
	}
}

func TestInvalidInputRejected(t *testing.T) {
	db := storeFunc(func(context.Context, string, string, int) ([]Order, error) {
		t.Error("store called with invalid input")
		return nil, nil
	})
	cs := serve(t, db, newLimiter(4, time.Second))
	for _, args := range []map[string]any{
		{"status": "deleted"},                       // not in the enum
		{"status": "open", "limit": 1000},           // above maximum
		{"status": "open", "owner": "bob"},          // unknown property
		{"status": "open' OR 1=1 --", "limit": 1.5}, // injection attempt, wrong type
	} {
		res, err := search(t.Context(), cs, args)
		if err != nil || !res.IsError {
			t.Errorf("args %v: err=%v isError=%v, want a tool error result", args, err, res != nil && res.IsError)
		}
	}
}

func TestClientCancellationReachesStore(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan error, 1)
	db := storeFunc(func(ctx context.Context, _, _ string, _ int) ([]Order, error) {
		close(started)
		<-ctx.Done()
		stopped <- ctx.Err()
		return nil, ctx.Err()
	})
	cs := serve(t, db, newLimiter(4, time.Minute))
	ctx, cancel := context.WithCancel(t.Context())
	go func() { <-started; cancel() }()
	if _, err := search(ctx, cs, map[string]any{"status": "open"}); !errors.Is(err, context.Canceled) {
		t.Errorf("call err = %v, want context.Canceled", err)
	}
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("store ctx err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("store kept running after the client cancelled")
	}
}
