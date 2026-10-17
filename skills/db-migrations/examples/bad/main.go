// Command server migrates the database on every start, then serves.
package main

import (
	"database/sql"
	"embed"
	"log"
	"net/http"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var embedMigrations embed.FS

// MISTAKE: SELECT * in application code. After any ALTER that adds or drops a
// column, cached prepared statements fail with "cached plan must not change
// result type", and rows.Scan into a fixed list of fields breaks.
const selectUser = `SELECT * FROM users WHERE id = $1`

func main() {
	// MISTAKE: the service DSN, usually a transaction pooler. A migration that
	// runs SET lock_timeout outside a transaction loses it between statements.
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}

	// MISTAKE: every replica migrates on startup, with no locker. Ten pods race
	// for the same DDL, and a slow migration fails liveness probes, so the
	// rollout loops in CrashLoopBackOff with the table locked.
	// MISTAKE: package-level goose state (SetBaseFS, SetDialect) and no timeout:
	// goose.Up uses context.Background, so a blocked ALTER hangs startup forever.
	goose.SetBaseFS(embedMigrations)
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatal(err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		var id, email, name, created string
		// Scans four columns by position; breaks when the table gains a fifth.
		if err := db.QueryRowContext(r.Context(), selectUser, r.PathValue("id")).
			Scan(&id, &email, &name, &created); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte(name))
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
