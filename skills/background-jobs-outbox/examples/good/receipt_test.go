package main

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type fakeStore struct {
	sent    map[int64]bool
	markErr error
}

func (s *fakeStore) ReceiptSent(_ context.Context, id int64) (bool, error) { return s.sent[id], nil }

func (s *fakeStore) MarkReceiptSent(_ context.Context, id int64) error {
	if s.markErr != nil {
		return s.markErr
	}
	s.sent[id] = true
	return nil
}

// fakeMailer deduplicates on the key, like a provider's idempotency API, and
// counts what a customer would actually receive.
type fakeMailer struct {
	err       error
	keys      map[string]bool
	delivered int
}

func (m *fakeMailer) Send(_ context.Context, _ int64, key string) error {
	if m.err != nil {
		return m.err
	}
	if !m.keys[key] {
		m.keys[key] = true
		m.delivered++
	}
	return nil
}

func job(orderID int64, attempt int) *river.Job[SendReceiptArgs] {
	return &river.Job[SendReceiptArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt},
		Args:   SendReceiptArgs{OrderID: orderID},
	}
}

func newWorker() (*SendReceiptWorker, *fakeStore, *fakeMailer) {
	s := &fakeStore{sent: map[int64]bool{}}
	m := &fakeMailer{keys: map[string]bool{}}
	return &SendReceiptWorker{store: s, mailer: m}, s, m
}

// At-least-once delivery means the same job can run again; the customer
// still gets one receipt, including after a crash between Send and the mark.
func TestReceiptIsIdempotent(t *testing.T) {
	w, s, m := newWorker()
	s.markErr = errors.New("connection reset")
	if err := w.Work(t.Context(), job(7, 1)); err == nil {
		t.Fatal("attempt 1: want the mark error so River retries")
	}
	s.markErr = nil
	for attempt := 2; attempt <= 3; attempt++ {
		if err := w.Work(t.Context(), job(7, attempt)); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if m.delivered != 1 {
		t.Fatalf("delivered %d receipts, want 1", m.delivered)
	}
}

func TestReceiptErrorsChooseRetryOrCancel(t *testing.T) {
	w, _, m := newWorker()
	m.err = errors.New("503 from mail API")
	err := w.Work(t.Context(), job(8, 1))
	if _, ok := errors.AsType[*river.JobCancelError](err); err == nil || ok {
		t.Fatalf("transient failure: err = %v, want a plain error that River retries", err)
	}
	m.err = ErrInvalidAddress
	err = w.Work(t.Context(), job(8, 2))
	if _, ok := errors.AsType[*river.JobCancelError](err); !ok || !errors.Is(err, ErrInvalidAddress) {
		t.Fatalf("permanent failure: err = %v, want JobCancel wrapping ErrInvalidAddress", err)
	}
}
