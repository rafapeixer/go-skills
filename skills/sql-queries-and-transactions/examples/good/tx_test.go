package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/jackc/pgx/v5/pgconn"
)

func pgErr(code string) error { return &pgconn.PgError{Code: code} }

// The fake attempt stands in for pgx.BeginTxFunc: it returns the queued errors
// in order and then succeeds, so no database is needed. synctest makes the
// backoff sleeps instant.
func TestRetryTx(t *testing.T) {
	tests := []struct {
		name      string
		errs      []error
		wantCalls int
		wantErr   bool
	}{
		{"commits on first attempt", nil, 1, false},
		{"reruns after serialization failure", []error{pgErr("40001")}, 2, false},
		{"reruns after deadlock wrapped by fn", []error{fmt.Errorf("debit account: %w", pgErr("40P01"))}, 2, false},
		{"unique violation is returned", []error{pgErr("23505")}, 1, true},
		{"statement timeout is returned", []error{pgErr("57014")}, 1, true},
		{"plain error is returned", []error{errors.New("encode event")}, 1, true},
		{"gives up after max attempts", []error{pgErr("40001"), pgErr("40001"), pgErr("40001"), pgErr("40001"), pgErr("40001")}, maxAttempts, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				err := retryTx(t.Context(), func() error {
					calls++
					if calls <= len(tt.errs) {
						return tt.errs[calls-1]
					}
					return nil
				})
				if calls != tt.wantCalls {
					t.Errorf("attempts = %d, want %d", calls, tt.wantCalls)
				}
				if (err != nil) != tt.wantErr {
					t.Errorf("err = %v, want error: %v", err, tt.wantErr)
				}
				if tt.wantErr && len(tt.errs) > 0 && !errors.Is(err, tt.errs[len(tt.errs)-1]) {
					t.Errorf("err = %v, want it to wrap the last attempt's error", err)
				}
			})
		})
	}
}

func TestRetryTxStopsWhenContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		err := retryTx(ctx, func() error {
			calls++
			cancel() // The request ends while the transaction is failing.
			return pgErr("40001")
		})
		if calls != 1 || !errors.Is(err, context.Canceled) {
			t.Fatalf("attempts = %d, err = %v; want 1 attempt and context.Canceled", calls, err)
		}
	})
}
