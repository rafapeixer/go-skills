package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.W.ExecContext(t.Context(), schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func pragma(t *testing.T, c *sql.Conn, name string) string {
	t.Helper()
	var v string
	if err := c.QueryRowContext(t.Context(), "PRAGMA "+name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

func TestPragmasOnEveryConnection(t *testing.T) {
	db := openTest(t, filepath.Join(t.TempDir(), "app.db"))
	want := map[string]string{"journal_mode": "wal", "busy_timeout": "5000", "synchronous": "1", "foreign_keys": "1"}

	// Holding the conns at once forces the pool to open distinct connections.
	var conns []*sql.Conn
	for range 4 {
		c, err := db.R.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		conns = append(conns, c)
	}
	w, err := db.W.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i, c := range append(conns, w) {
		for name, v := range want {
			if got := pragma(t, c, name); got != v {
				t.Errorf("conn %d: PRAGMA %s = %s, want %s", i, name, got, v)
			}
		}
	}
	if got := pragma(t, conns[0], "query_only"); got != "1" {
		t.Errorf("reader query_only = %s, want 1", got)
	}
}

func TestWriterPoolHasOneConnection(t *testing.T) {
	db := openTest(t, filepath.Join(t.TempDir(), "app.db"))
	if got := db.W.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer MaxOpenConnections = %d, want 1", got)
	}
	c, err := db.W.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := db.W.Conn(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second writer conn: err = %v, want DeadlineExceeded", err)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	db := openTest(t, filepath.Join(t.TempDir(), "app.db"))
	_, err := db.W.ExecContext(t.Context(), "INSERT INTO hits (counter_id) VALUES (999)")
	if err == nil {
		t.Fatal("insert with a missing parent succeeded; foreign_keys is off")
	}
}
