package main

import (
	"path/filepath"
	"testing"
)

// In WAL mode a reader is not blocked by an open write transaction; it reads
// the last committed state.
func TestReadsProceedDuringWrite(t *testing.T) {
	db := openTest(t, filepath.Join(t.TempDir(), "app.db"))
	count := func() (n int64) {
		if err := db.R.QueryRowContext(t.Context(), "SELECT count(*) FROM hits").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	tx, err := db.W.BeginTx(t.Context(), nil) // BEGIN IMMEDIATE holds the write lock
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO hits (counter_id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 { // does not block, and sees the last committed state
		t.Fatalf("count during write = %d, want 0", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("count after commit = %d, want 1", n)
	}
}
