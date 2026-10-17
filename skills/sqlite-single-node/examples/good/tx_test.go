package main

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Two DBs on one file stand in for two processes (the service and an admin
// CLI): their writers really compete for the file's write lock.
func TestConcurrentWritersDoNotGetBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	dbs := []*DB{openTest(t, path), openTest(t, path)}

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for range 20 {
				if err := increment(t.Context(), dbs[g%2], 1); err != nil {
					t.Error(err) // with a deferred BEGIN, about half fail with SQLITE_BUSY
				}
			}
		})
	}
	wg.Wait()

	var n int64
	if err := dbs[0].R.QueryRowContext(t.Context(), "SELECT n FROM counters WHERE id = 1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 160 {
		t.Fatalf("counter = %d, want 160", n)
	}
}

// A deferred transaction that reads first cannot upgrade once another
// connection commits, and SQLite fails it at once instead of waiting.
func TestDeferredUpgradeFailsDespiteBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db := openTest(t, path)
	deferred, err := sql.Open("sqlite", dsn(path, nil)) // same pragmas, no _txlock
	if err != nil {
		t.Fatal(err)
	}
	defer deferred.Close()

	tx, err := deferred.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int64
	if err := tx.QueryRowContext(t.Context(), "SELECT n FROM counters WHERE id = 1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := increment(t.Context(), db, 1); err != nil { // another writer commits
		t.Fatal(err)
	}

	start := time.Now()
	_, err = tx.ExecContext(t.Context(), "UPDATE counters SET n = ? WHERE id = 1", n+1)
	sqlErr, ok := errors.AsType[*sqlite.Error](err)
	if !ok || sqlErr.Code()&0xff != sqlite3.SQLITE_BUSY {
		t.Fatalf("upgrade: err = %v, want SQLITE_BUSY", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("upgrade failed after %v; busy_timeout should not apply", d)
	}
}
