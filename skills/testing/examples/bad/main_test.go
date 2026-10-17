package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// MISTAKE: one copy-pasted function per input instead of a table with t.Run;
// cases drift apart and a new edge case means another near-duplicate function.
func TestParseAmountWhole(t *testing.T) {
	got, _ := parseAmount("12")
	if got != 1200 {
		t.Error("wrong")
	}
}

func TestParseAmountDecimals(t *testing.T) {
	// MISTAKE: the error is discarded and the message says neither input nor result;
	// "1.15" becomes 114 through float rounding and no test or fuzz target ever tries it.
	got, _ := parseAmount("12.34")
	if got != 1234 {
		t.Error("wrong")
	}
}

func TestConvertRetries(t *testing.T) {
	calls := 0
	// MISTAKE: swapping a global; a parallel test, or a test that forgets to restore it,
	// sees this fake instead of the real function.
	fetchRate = func(string) (float64, error) {
		calls++
		if calls < 3 {
			return 0, errors.New("upstream down")
		}
		return 0.5, nil
	}
	start := time.Now()
	got, err := convert(1000, "EUR")
	// MISTAKE: the retries really sleep, so this test takes 2s of wall clock, and an
	// assertion on elapsed time would be flaky on a loaded CI runner. Use synctest.Test.
	if err != nil || got != 500 || time.Since(start) < 2*time.Second {
		t.Fatalf("convert() = %d, %v", got, err)
	}
	// MISTAKE: asserting the exact call count tests how convert works, not what it
	// returns; changing the retry policy breaks this test even when behavior is right.
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

// MISTAKE: calling the handler function directly skips the mux, so method and route
// patterns, middleware and the real server timeouts are never exercised.
func TestHandleConvert(t *testing.T) {
	rec := httptest.NewRecorder()
	handleConvert(rec, httptest.NewRequest(http.MethodPost, "/convert?amount=10&currency=EUR", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func BenchmarkParseAmount(b *testing.B) {
	// MISTAKE: the b.N loop discards the result, so the compiler may delete the call
	// and the benchmark measures an empty loop. Use for b.Loop().
	for i := 0; i < b.N; i++ {
		parseAmount("1234567.89")
	}
}
