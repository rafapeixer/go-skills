// Command bad opens SQLite the way first attempts do: fine in a demo, broken under concurrent writes.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	// MISTAKE (described, not imported here): reaching for github.com/mattn/go-sqlite3
	// by habit, which needs cgo, so CGO_ENABLED=0 and distroless/static builds break,
	// or for crawshaw.io/sqlite, whose last release is from 2020 and carries none of
	// SQLite's fixes since. Neither choice was measured; the cgo-free driver is the default.
	_ "modernc.org/sqlite"
)

func open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// MISTAKE: one pool for reads and writes, with many connections. Every one of
	// them may write, so concurrent writers fight over the single write lock.
	db.SetMaxOpenConns(32)
	// MISTAKE: PRAGMAs sent once through the pool run on one connection. WAL is
	// stored in the file, but busy_timeout, synchronous and foreign_keys are per
	// connection: every other connection has busy_timeout=0, fails at once with
	// "database is locked", and accepts rows that violate foreign keys.
	for _, p := range []string{"journal_mode=WAL", "busy_timeout=5000", "synchronous=NORMAL", "foreign_keys=ON"} {
		if _, err := db.Exec("PRAGMA " + p); err != nil {
			return nil, err
		}
	}
	return db, nil
}

func withdraw(ctx context.Context, db *sql.DB, id, cents int64) error {
	// MISTAKE: a plain BeginTx sends a deferred BEGIN. The SELECT takes a read
	// snapshot; if another connection commits first, the UPDATE cannot upgrade
	// and fails with SQLITE_BUSY immediately, without consulting busy_timeout.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var balance int64
	if err := tx.QueryRowContext(ctx, "SELECT balance FROM accounts WHERE id = ?", id).Scan(&balance); err != nil {
		return err
	}
	if balance < cents {
		return errors.New("insufficient funds")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE accounts SET balance = ? WHERE id = ?", balance-cents, id); err != nil {
		return err
	}
	return tx.Commit()
}

// MISTAKE: backing up by copying the live file. Recent commits are still in
// app.db-wal, so the copy misses them or is torn mid-checkpoint; use Litestream.
func backup(path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(path + ".bak")
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return errors.Join(err, dst.Close())
}

func main() {
	ctx := context.Background()
	db, err := open("app.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.Exec("CREATE TABLE IF NOT EXISTS accounts (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL)")

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := withdraw(ctx, db, 1, 1); err != nil {
				fmt.Println("withdraw:", err) // database is locked (5) (SQLITE_BUSY)
			}
		})
	}
	wg.Wait()
	if err := backup("app.db"); err != nil {
		log.Fatal(err)
	}
}
