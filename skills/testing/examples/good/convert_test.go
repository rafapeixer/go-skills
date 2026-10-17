package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// fakeRates is a hand-written fake: the first `hang` calls block until the
// attempt's context expires, like a stalled upstream, then it answers.
type fakeRates struct {
	hang  int
	calls int
}

func (f *fakeRates) Rate(ctx context.Context, _ string) (float64, error) {
	f.calls++
	if f.calls <= f.hang {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return 0.5, nil
}

func TestConvertRetries(t *testing.T) {
	tests := []struct {
		name        string
		hang        int
		want        int64
		wantErr     error
		wantElapsed time.Duration
	}{
		{"first attempt", 0, 500, nil, 0},
		{"two timeouts then success", 2, 500, nil, 6 * time.Second},
		{"every attempt times out", 3, 0, context.DeadlineExceeded, 8 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rates := &fakeRates{hang: tt.hang}
				c := &Converter{Rates: rates, Timeout: 2 * time.Second, Attempts: 3, Backoff: time.Second}
				start := time.Now()

				got, err := c.Convert(t.Context(), 1000, "EUR")

				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Convert() error = %v, want %v", err, tt.wantErr)
				}
				if got != tt.want {
					t.Errorf("Convert() = %d, want %d", got, tt.want)
				}
				// The fake clock makes elapsed time exact: 2s timeout + 1s backoff per retry.
				if elapsed := time.Since(start); elapsed != tt.wantElapsed {
					t.Errorf("elapsed = %v, want %v", elapsed, tt.wantElapsed)
				}
			})
		})
	}
}
