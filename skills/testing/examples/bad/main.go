// Command bad converts amounts like the good example, but its shape makes the
// tests in main_test.go slow, order-dependent and blind to real failures.
package main

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

// MISTAKE: a package-level function variable is the only seam for tests;
// tests overwrite global state, so they cannot run in parallel and leak into each other.
var fetchRate = func(currency string) (float64, error) {
	if currency == "EUR" {
		return 0.92, nil
	}
	return 0, errors.New("unknown currency")
}

func convert(cents int64, currency string) (int64, error) {
	var err error
	for range 3 {
		var rate float64
		if rate, err = fetchRate(currency); err == nil {
			return int64(math.Round(float64(cents) * rate)), nil
		}
		// MISTAKE: real sleep with no context; tests can only wait the backoff out on the wall clock.
		time.Sleep(time.Second)
	}
	return 0, fmt.Errorf("rate %s: %w", currency, err)
}

// MISTAKE: an untrusted-input parser with no fuzz test; ParseFloat accepts "-5",
// "1e3" and "NaN", and float rounding turns "1.15" into 114 cents.
func parseAmount(s string) (int64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse amount %q: %w", s, err)
	}
	return int64(f * 100), nil
}

func handleConvert(w http.ResponseWriter, r *http.Request) {
	cents, err := parseAmount(r.URL.Query().Get("amount"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out, err := convert(cents, r.URL.Query().Get("currency"))
	if err != nil {
		http.Error(w, "rate unavailable", http.StatusBadGateway)
		return
	}
	fmt.Fprintf(w, "%d\n", out)
}

func main() {
	http.HandleFunc("/convert", handleConvert)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Println(err)
	}
}
