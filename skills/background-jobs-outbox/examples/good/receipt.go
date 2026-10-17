package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
)

// SendReceiptArgs is the job payload. It carries the order ID, not the email
// body, so a retry reads current data and the row stays small.
type SendReceiptArgs struct {
	OrderID int64 `json:"order_id"`
}

func (SendReceiptArgs) Kind() string { return "send_receipt" }

// InsertOpts caps attempts for this kind: after the fifth failure River moves
// the job to the discarded state instead of retrying it for weeks.
func (SendReceiptArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: queueReceipts, MaxAttempts: 5}
}

// ErrInvalidAddress is permanent: retrying cannot fix it.
var ErrInvalidAddress = errors.New("invalid email address")

type receiptStore interface {
	ReceiptSent(ctx context.Context, orderID int64) (bool, error)
	MarkReceiptSent(ctx context.Context, orderID int64) error
}

type mailer interface {
	// Send must deduplicate on idempotencyKey; most email and payment APIs do.
	Send(ctx context.Context, orderID int64, idempotencyKey string) error
}

type SendReceiptWorker struct {
	river.WorkerDefaults[SendReceiptArgs]
	store  receiptStore
	mailer mailer
}

// Timeout bounds one attempt; a hung mail API frees its worker slot.
func (w *SendReceiptWorker) Timeout(*river.Job[SendReceiptArgs]) time.Duration {
	return 30 * time.Second
}

// Work is idempotent: River delivers at least once, so a job can run again
// after a crash between Send and the completion River records.
func (w *SendReceiptWorker) Work(ctx context.Context, job *river.Job[SendReceiptArgs]) error {
	id := job.Args.OrderID
	sent, err := w.store.ReceiptSent(ctx, id)
	if err != nil {
		return fmt.Errorf("check receipt %d: %w", id, err)
	}
	if sent {
		return nil // An earlier attempt finished the work.
	}
	// The key is derived from the order, not the attempt, so a retry after a
	// crash between Send and MarkReceiptSent is deduplicated by the provider.
	key := fmt.Sprintf("receipt-%d", id)
	if err := w.mailer.Send(ctx, id, key); err != nil {
		if errors.Is(err, ErrInvalidAddress) {
			return river.JobCancel(fmt.Errorf("send receipt %d: %w", id, err))
		}
		return fmt.Errorf("send receipt %d: %w", id, err) // retried with backoff
	}
	if err := w.store.MarkReceiptSent(ctx, id); err != nil {
		return fmt.Errorf("mark receipt %d: %w", id, err)
	}
	return nil
}

// logMailer stands in for a real mail API client.
type logMailer struct{}

func (logMailer) Send(ctx context.Context, orderID int64, key string) error {
	slog.InfoContext(ctx, "send receipt", "order_id", orderID, "idempotency_key", key)
	return nil
}
