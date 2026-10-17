package main

import (
	"log/slog"
	"testing"
)

func TestRun(t *testing.T) {
	if err := run(t.Context(), slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("run: %v", err)
	}
}
