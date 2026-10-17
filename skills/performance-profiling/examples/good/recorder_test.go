package main

import (
	"os"
	"testing"
	"time"
)

// Only one flight recorder may be active per process, so this is the only
// test that starts one, and it does not run in parallel.
func TestRecorderSnapshot(t *testing.T) {
	dir := t.TempDir()
	rec, err := startRecorder(dir, 300*time.Millisecond, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rec.stop)

	path, err := rec.snapshot()
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("snapshot file: %v", err)
	}
	if info.Size() == 0 {
		t.Errorf("snapshot %s is empty", path)
	}

	// Within the cooldown a second slow request writes nothing.
	if again, err := rec.snapshot(); err != nil || again != "" {
		t.Errorf("second snapshot() = %q, %v; want \"\", nil", again, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("trace files = %d, want 1", len(entries))
	}
}
