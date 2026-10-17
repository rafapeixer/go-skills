package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	o := Order{ID: 42, CreatedAt: time.Date(2026, 10, 17, 9, 30, 0, 123456000, time.UTC)}
	createdAt, id, err := decodeCursor(encodeCursor(o))
	if err != nil {
		t.Fatal(err)
	}
	if !createdAt.Equal(o.CreatedAt) || id != o.ID {
		t.Errorf("decoded (%v, %d), want (%v, %d)", createdAt, id, o.CreatedAt, o.ID)
	}
}

func TestDecodeCursorRejects(t *testing.T) {
	for _, token := range []string{"not base64!", "MTIz", "YWJjLjQy", "MTIzLnh5"} {
		if _, _, err := decodeCursor(token); !errors.Is(err, ErrBadCursor) {
			t.Errorf("decodeCursor(%q) err = %v, want ErrBadCursor", token, err)
		}
	}
}

func TestListOrdersSQL(t *testing.T) {
	tests := []struct {
		sort      string
		hasCursor bool
		want      []string
	}{
		{"newest", false, []string{"ORDER BY created_at DESC, id DESC LIMIT $2"}},
		{"newest", true, []string{"(created_at, id) < ($3, $4)", "ORDER BY created_at DESC, id DESC"}},
		{"oldest", true, []string{"(created_at, id) > ($3, $4)", "ORDER BY created_at ASC, id ASC"}},
	}
	for _, tt := range tests {
		query, err := listOrdersSQL(tt.sort, tt.hasCursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range tt.want {
			if !strings.Contains(query, part) {
				t.Errorf("listOrdersSQL(%q, %v) = %q, want it to contain %q", tt.sort, tt.hasCursor, query, part)
			}
		}
		if strings.Contains(query, "OFFSET") {
			t.Errorf("listOrdersSQL(%q, %v) uses OFFSET", tt.sort, tt.hasCursor)
		}
	}
}

// Anything outside the allow-list is rejected, so request text never reaches the SQL.
func TestListOrdersSQLRejectsUnknownSort(t *testing.T) {
	for _, sort := range []string{"", "created_at", "id; DROP TABLE orders", "NEWEST"} {
		if _, err := listOrdersSQL(sort, false); !errors.Is(err, ErrBadSort) {
			t.Errorf("listOrdersSQL(%q) err = %v, want ErrBadSort", sort, err)
		}
	}
}
