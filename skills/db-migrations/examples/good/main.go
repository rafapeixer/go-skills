// Command migrate applies the embedded migrations and exits. Run it as a
// Kubernetes Job (or a CI/CD step) before rolling out the release; the
// service binary never migrates on startup.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fset := flag.NewFlagSet("migrate", flag.ContinueOnError)
	timeout := fset.Duration("timeout", 10*time.Minute, "upper bound for the whole run")
	if err := fset.Parse(args); err != nil {
		return err
	}
	// MIGRATE_DATABASE_URL points at Postgres directly, not at a transaction
	// pooler: session SET and the advisory lock must stay on one connection.
	dsn := os.Getenv("MIGRATE_DATABASE_URL")
	if dsn == "" {
		return errors.New("MIGRATE_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	p, err := newProvider(db)
	if err != nil {
		return err
	}
	results, err := p.Up(ctx)
	for _, r := range results {
		slog.Info("migration", "result", r.String())
	}
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
