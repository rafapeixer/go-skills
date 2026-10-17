package main

import (
	"database/sql"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// No test here connects: sql.Open only validates the driver name, and goose
// lists sources without touching the database.
func TestGooseSeesEveryFile(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://migrate@db.invalid/app")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := newProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	files, err := fs.Glob(embedded, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	// goose silently skips files without a NUMBER_ prefix.
	sources := p.ListSources()
	if len(sources) != len(files) {
		t.Fatalf("goose found %d migrations, the folder has %d .sql files", len(sources), len(files))
	}
	for i, s := range sources {
		if want := int64(i + 1); s.Version != want {
			t.Errorf("%s: version %d, want %d (sequential, no gaps)", s.Path, s.Version, want)
		}
	}
}

func TestMigrationConventions(t *testing.T) {
	files, err := fs.Glob(embedded, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		b, err := fs.ReadFile(embedded, name)
		if err != nil {
			t.Fatal(err)
		}
		sql := string(b)
		noTx := strings.Contains(sql, "-- +goose NO TRANSACTION")
		if strings.Contains(sql, "CONCURRENTLY") && !noTx {
			t.Errorf("%s: CONCURRENTLY needs -- +goose NO TRANSACTION", name)
		}
		up, down, ok := strings.Cut(sql, "-- +goose Down")
		if !ok {
			t.Errorf("%s: no -- +goose Down section", name)
		}
		// Up and Down run as separate transactions, so each sets its own timeout.
		want := "SET LOCAL lock_timeout"
		if noTx {
			want = "SET lock_timeout" // no transaction, so SET LOCAL would be a no-op
		}
		for section, body := range map[string]string{"Up": up, "Down": down} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: %s section has no %s", name, section, want)
			}
		}
	}
}

var selectStar = regexp.MustCompile(`(?i)select\s+\*`)

func TestQueriesNameColumns(t *testing.T) {
	if selectStar.MatchString(selectUser) {
		t.Errorf("selectUser uses SELECT *: %s", selectUser)
	}
}
