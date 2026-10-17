// Command bad compiles, and each marked line is a query or transaction
// pattern that works in a demo and fails under real data, load or input.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Order struct {
	ID         int64
	CustomerID int64
	TotalCents int64
}

func listOrders(ctx context.Context, pool *pgxpool.Pool, customerID, sort string, page int) ([]Order, error) {
	// MISTAKE: request text pasted into the SQL. sort=id;DROP TABLE orders-- or
	// customer=0 OR 1=1 runs as SQL; quoting by hand does not fix it.
	// MISTAKE: OFFSET reads and throws away every earlier row, so page 5000 scans
	// 100,000 rows, and an insert between requests shifts rows across pages.
	query := fmt.Sprintf("SELECT * FROM orders WHERE customer_id = %s ORDER BY %s LIMIT 20 OFFSET %d",
		customerID, sort, page*20)
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var orders []Order
	for rows.Next() {
		var o Order
		// MISTAKE: hand-written Scan over SELECT *. Adding a column to orders makes
		// every call fail at run time; no compiler or generator catches it.
		if err := rows.Scan(&o.ID, &o.CustomerID, &o.TotalCents); err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func totals(ctx context.Context, pool *pgxpool.Pool, customerIDs []int64) (map[int64]int64, error) {
	sums := make(map[int64]int64)
	// MISTAKE: one query per customer (N+1). A page of 100 customers costs 100
	// round trips and holds a pool connection for all of them.
	for _, id := range customerIDs {
		var sum int64
		err := pool.QueryRow(ctx, "SELECT coalesce(sum(total_cents), 0) FROM orders WHERE customer_id = $1", id).Scan(&sum)
		if err != nil {
			return nil, err
		}
		sums[id] = sum
	}
	return sums, nil
}

func placeOrder(ctx context.Context, pool *pgxpool.Pool, customerID, cents int64) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	q := "INSERT INTO orders (customer_id, total_cents) VALUES ($1, $2) RETURNING id"
	if err := tx.QueryRow(ctx, q, customerID, cents).Scan(&id); err != nil {
		// MISTAKE: retrying one statement after an error. The transaction is already
		// aborted, so this fails with 25P02 and a deadlock (40P01) is never recovered.
		if strings.Contains(err.Error(), "deadlock") {
			err = tx.QueryRow(ctx, q, customerID, cents).Scan(&id)
		}
		if err != nil {
			return err
		}
	}
	// MISTAKE: an HTTP call inside the transaction. The row locks and the connection
	// are held for the mail service's latency, and a later rollback cannot unsend it.
	body := strings.NewReader(fmt.Sprintf(`{"order_id":%d}`, id))
	resp, err := http.Post("https://mail.internal/send", "application/json", body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	// MISTAKE: no statement_timeout or idle_in_transaction_session_timeout on the role;
	// if this pod hangs here, Postgres keeps the transaction and its locks open.
	return tx.Commit(ctx)
}

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	_, _ = listOrders(ctx, pool, "7", "created_at desc", 3)
	_, _ = totals(ctx, pool, []int64{7, 8, 9})
	_ = placeOrder(ctx, pool, 7, 2599)
}
