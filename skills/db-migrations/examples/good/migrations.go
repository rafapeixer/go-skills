package main

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// The migrations ship inside the binary, so the Job runs exactly the SQL that
// was reviewed and built with this release.
//
//go:embed migrations/*.sql
var embedded embed.FS

// newProvider builds a goose provider over the embedded files. It does not
// connect: goose reads the file list now and parses each file when it runs.
func newProvider(db *sql.DB) (*goose.Provider, error) {
	fsys, err := fs.Sub(embedded, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	// A session advisory lock makes a second runner wait instead of racing
	// the first. It needs a direct (or session-pooled) connection.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("create migration locker: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("create migration provider: %w", err)
	}
	return p, nil
}
