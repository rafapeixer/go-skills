// Package main shows the background-job mistakes that pass a demo: a queue
// built on NOTIFY, enqueues outside the business transaction, a claim
// without SKIP LOCKED, a handler that is not idempotent, and a goroutine
// per job.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

type SendReceiptArgs struct {
	OrderID int64 `json:"order_id"`
}

func (SendReceiptArgs) Kind() string { return "send_receipt" }

func placeOrder(ctx context.Context, pool *pgxpool.Pool, client *river.Client[pgx.Tx], customerID int64) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, "INSERT INTO orders (customer_id) VALUES ($1) RETURNING id", customerID).Scan(&id); err != nil {
		return err
	}
	// MISTAKE: enqueued on the pool, outside tx, before commit. If the commit
	// fails the job still exists: a receipt for an order that was never placed.
	if _, err := pool.Exec(ctx, "INSERT INTO jobs (order_id) VALUES ($1)", id); err != nil {
		return err
	}
	// MISTAKE: NOTIFY as the queue. With no listener connected (deploy, crash,
	// PgBouncer in transaction mode) this message is gone for good.
	if _, err := tx.Exec(ctx, "SELECT pg_notify('receipts', $1)", strconv.FormatInt(id, 10)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// MISTAKE: enqueued after commit with Insert instead of InsertTx. A crash
	// or a failed insert here loses the job while the order exists.
	_, err = client.Insert(ctx, SendReceiptArgs{OrderID: id}, nil)
	return err
}

// MISTAKE: the notification payload is treated as the job. Nothing records
// that it was handled, so a failure here drops the work silently.
func listenForReceipts(ctx context.Context, conn *pgx.Conn) {
	conn.Exec(ctx, "LISTEN receipts")
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return
		}
		id, _ := strconv.ParseInt(n.Payload, 10, 64)
		sendReceipt(ctx, id)
	}
}

func pollJobs(ctx context.Context, pool *pgxpool.Pool) error {
	// MISTAKE: no FOR UPDATE SKIP LOCKED. Every replica reads the same rows
	// and sends the same receipts; FOR UPDATE alone would serialize them.
	rows, err := pool.Query(ctx, "SELECT id, order_id FROM jobs WHERE done = false ORDER BY id LIMIT 100")
	if err != nil {
		return err
	}
	var ids, orders []int64
	for rows.Next() {
		var id, orderID int64
		rows.Scan(&id, &orderID)
		ids, orders = append(ids, id), append(orders, orderID)
	}
	for i := range ids {
		// MISTAKE: one goroutine per job with no limit and no owner. A backlog of
		// 10,000 rows opens 10,000 queries against a 20-connection pool, and
		// shutdown kills them mid-send.
		go func() {
			// MISTAKE: not idempotent. A crash between the send and the UPDATE,
			// or a second replica, sends the receipt again.
			sendReceipt(ctx, orders[i])
			pool.Exec(ctx, "UPDATE jobs SET done = true WHERE id = $1", ids[i])
		}()
	}
	return nil
}

func sendReceipt(_ context.Context, orderID int64) { fmt.Println("receipt for order", orderID) }

func main() {
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	_ = pollJobs(context.Background(), pool)
}
