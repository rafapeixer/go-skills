package main

import (
	"os"
	"strings"
	"testing"
)

// The page's LIMIT must run inside the subquery, before the LATERAL join, so
// items are looked up only for the rows of the page.
func TestOrderPageWithItemsLimitsBeforeLateral(t *testing.T) {
	limit := strings.Index(orderPageWithItems, "LIMIT $2")
	closeSub := strings.Index(orderPageWithItems, ") o")
	lateral := strings.Index(orderPageWithItems, "LEFT JOIN LATERAL")
	if limit < 0 || closeSub < 0 || lateral < 0 || !(limit < closeSub && closeSub < lateral) {
		t.Errorf("want LIMIT inside the subquery, before LEFT JOIN LATERAL:\n%s", orderPageWithItems)
	}
	if strings.Count(orderPageWithItems, "LIMIT") != 1 || strings.Contains(orderPageWithItems, "OFFSET") {
		t.Errorf("want exactly one LIMIT and no OFFSET:\n%s", orderPageWithItems)
	}
	// Orders without items still come back, with empty arrays instead of NULL.
	for _, part := range []string{"coalesce(i.skus, '{}')", "coalesce(i.qtys, '{}')", ") i ON true"} {
		if !strings.Contains(orderPageWithItems, part) {
			t.Errorf("query does not contain %q", part)
		}
	}
}

// The stand-in for generated code must match the query sqlc reads.
func TestOrderPageWithItemsMatchesQueryFile(t *testing.T) {
	src, err := os.ReadFile("query.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(orderPageWithItems) + ";"
	if !strings.Contains(string(src), want) {
		t.Errorf("query.sql does not contain the OrderPageWithItems query as written in items.go")
	}
}
