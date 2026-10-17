package main

import (
	"go/version"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestFloorMatchesGoMod(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	var goLine string
	for line := range strings.Lines(string(data)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			goLine = "go" + v
		}
	}
	if goLine != floor {
		t.Fatalf("go.mod declares %q, floor constant is %q", goLine, floor)
	}
	if version.Lang(goLine) == goLine {
		t.Errorf("go line %q is a language version; declare a release such as go 1.26.0", goLine)
	}
}

func TestBuiltWithFloorOrNewer(t *testing.T) {
	if got := runtime.Version(); !atLeast(got, floor) {
		t.Fatalf("built with %s, below the %s floor", got, floor)
	}
}

func TestAtLeastComparesVersionsNotStrings(t *testing.T) {
	tests := []struct {
		toolchain string
		want      bool
	}{
		{"go1.26.0", true},
		{"go1.27.2", true},
		{"go1.25.14", false},
		{"go1.9", false}, // "go1.9" >= "go1.26.0" as strings, which is wrong.
		{"devel go1.28-abcdef", true},
	}
	for _, tt := range tests {
		if got := atLeast(tt.toolchain, floor); got != tt.want {
			t.Errorf("atLeast(%q, %q) = %t, want %t", tt.toolchain, floor, got, tt.want)
		}
	}
}

func TestEachIterationHasItsOwnVariable(t *testing.T) {
	got := probe([]string{"db", "cache", "queue"})
	want := []string{"ok db", "ok cache", "ok queue"}
	if !slices.Equal(got, want) {
		t.Fatalf("probe = %v, want %v", got, want)
	}
}
