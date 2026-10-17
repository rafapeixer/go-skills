package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestPutBufferDropsOversized(t *testing.T) {
	big := new(bytes.Buffer)
	big.Grow(maxPooledBuffer + 1)
	putBuffer(big)
	// The pool may drop any item, so this only proves the oversized buffer
	// never comes back; it does not assume a small one would.
	for range 10 {
		if b := getBuffer(); b == big {
			t.Fatalf("getBuffer() returned the %d-byte buffer that should have been dropped", big.Cap())
		}
	}
}

func TestWriteReport(t *testing.T) {
	var out strings.Builder
	if err := writeReport(&out, 2); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "row 0\nrow 1\n"; got != want {
		t.Errorf("writeReport(2) = %q, want %q", got, want)
	}
}

// Run with: go test -run '^$' -bench Report -benchmem -count 10 > new.txt
// and compare against the previous run with benchstat old.txt new.txt.
// B/op is the allocation cost of one request at this layer.
func BenchmarkWriteReport(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if err := writeReport(io.Discard, 100); err != nil {
			b.Fatal(err)
		}
	}
}
