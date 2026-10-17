// Command good shows a single-node service that owns one SQLite file: a
// writer connection, a read pool, and Litestream (litestream.yml) running next
// to it for continuous backup.
package main

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
)

const schema = `
CREATE TABLE IF NOT EXISTS counters (id INTEGER PRIMARY KEY, n INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS hits (
	id INTEGER PRIMARY KEY,
	counter_id INTEGER NOT NULL REFERENCES counters(id)
);
INSERT OR IGNORE INTO counters (id, n) VALUES (1, 0);`

// increment reads and then writes inside one IMMEDIATE transaction, so no
// other writer can commit between the SELECT and the UPDATE.
func increment(ctx context.Context, db *DB, id int64) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		var n int64
		if err := tx.QueryRowContext(ctx, "SELECT n FROM counters WHERE id = ?", id).Scan(&n); err != nil {
			return fmt.Errorf("read counter %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE counters SET n = ? WHERE id = ?", n+1, id); err != nil {
			return fmt.Errorf("update counter %d: %w", id, err)
		}
		return nil
	})
}

func main() {
	ctx := context.Background()
	// The file lives on the node's local disk (a persistent volume), never on NFS.
	db, err := Open(ctx, cmp.Or(os.Getenv("SQLITE_PATH"), "app.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if _, err := db.W.ExecContext(ctx, schema); err != nil {
		log.Fatal(fmt.Errorf("apply schema: %w", err))
	}
	if err := increment(ctx, db, 1); err != nil {
		log.Fatal(err)
	}
	var n int64
	if err := db.R.QueryRowContext(ctx, "SELECT n FROM counters WHERE id = 1").Scan(&n); err != nil {
		log.Fatal(fmt.Errorf("read counter 1: %w", err))
	}
	fmt.Println("counter:", n)
}
