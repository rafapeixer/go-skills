package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrBadSort   = errors.New("unknown sort order")
	ErrBadCursor = errors.New("malformed page cursor")
)

// sortSpec is SQL text written here, in code. A request picks a key of sorts;
// its text never reaches the query.
type sortSpec struct{ cmp, order string }

var sorts = map[string]sortSpec{
	"newest": {cmp: "<", order: "created_at DESC, id DESC"},
	"oldest": {cmp: ">", order: "created_at ASC, id ASC"},
}

// listOrdersSQL is the one query sqlc cannot express, because the ORDER BY
// varies. Values stay $n parameters; only allow-listed text is concatenated.
func listOrdersSQL(sort string, hasCursor bool) (string, error) {
	s, ok := sorts[sort]
	if !ok {
		return "", fmt.Errorf("list orders by %q: %w", sort, ErrBadSort)
	}
	where := "customer_id = $1"
	if hasCursor {
		// The row comparison matches the index (customer_id, created_at, id),
		// so every page is an index range scan, however deep it is.
		where += " AND (created_at, id) " + s.cmp + " ($3, $4)"
	}
	return "SELECT id, customer_id, total_cents, created_at FROM orders WHERE " +
		where + " ORDER BY " + s.order + " LIMIT $2", nil
}

// encodeCursor returns an opaque token for the position after o. Postgres
// stores timestamptz in microseconds, so UnixMicro loses nothing.
func encodeCursor(o Order) string {
	raw := strconv.FormatInt(o.CreatedAt.UnixMicro(), 10) + "." + strconv.FormatInt(o.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(token string) (createdAt time.Time, id int64, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	micros, idText, ok := strings.Cut(string(raw), ".")
	if err != nil || !ok {
		return time.Time{}, 0, ErrBadCursor
	}
	us, err1 := strconv.ParseInt(micros, 10, 64)
	id, err2 := strconv.ParseInt(idText, 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, 0, ErrBadCursor
	}
	return time.UnixMicro(us), id, nil
}

// ListOrders returns one page and the cursor for the next, empty on the last page.
func ListOrders(ctx context.Context, db DBTX, customerID int64, sort, after string, limit int) ([]Order, string, error) {
	args := []any{customerID, limit}
	if after != "" {
		createdAt, id, err := decodeCursor(after)
		if err != nil {
			return nil, "", fmt.Errorf("list orders: %w", err)
		}
		args = append(args, createdAt, id)
	}
	query, err := listOrdersSQL(sort, after != "")
	if err != nil {
		return nil, "", err
	}
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list orders for customer %d: %w", customerID, err)
	}
	orders, err := pgx.CollectRows(rows, pgx.RowToStructByName[Order])
	if err != nil {
		return nil, "", fmt.Errorf("list orders for customer %d: %w", customerID, err)
	}
	next := ""
	if len(orders) == limit {
		next = encodeCursor(orders[len(orders)-1])
	}
	return orders, next, nil
}
