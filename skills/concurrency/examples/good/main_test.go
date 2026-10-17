package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// synctest.Test waits for every goroutine in the bubble to exit and fails on
// deadlock, so each test below also proves that nothing leaked.

func TestPricesRespectsLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var active, peak int
		lookup := func(ctx context.Context, sku string) (Price, error) {
			mu.Lock()
			active++
			peak = max(peak, active)
			mu.Unlock()
			time.Sleep(time.Second)
			mu.Lock()
			active--
			mu.Unlock()
			return Price{SKU: sku}, nil
		}
		skus := make([]string, 50)
		if _, err := prices(t.Context(), skus, lookup); err != nil {
			t.Fatal(err)
		}
		if got := peak; got > maxDBConns {
			t.Errorf("peak concurrent lookups = %d, want <= %d", got, maxDBConns)
		}
	})
}

func TestPricesCancelsSiblings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lookup := func(ctx context.Context, sku string) (Price, error) {
			if sku == "bad" {
				return Price{}, ErrNotFound
			}
			<-ctx.Done() // would block forever if the failure did not cancel ctx
			return Price{}, ctx.Err()
		}
		_, err := prices(t.Context(), []string{"a", "b", "c", "bad", "d", "e"}, lookup)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestHandlerStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logger := slog.New(slog.DiscardHandler)
		a := startAuditor(logger, 8)
		defer a.Stop() // without it the auditor goroutine outlives the test and synctest fails
		h := pricesHandler(&cache{m: map[string]Price{}}, a, logger)

		for query, want := range map[string]int{
			"sku=pen&sku=ink": http.StatusOK,
			"sku=pen&sku=":    http.StatusNotFound,
		} {
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest(http.MethodGet, "/prices?"+query, nil))
			if rec.Code != want {
				t.Errorf("GET /prices?%s = %d, want %d", query, rec.Code, want)
			}
		}
	})
}

func TestRecordDoesNotBlockWhenFull(t *testing.T) {
	a := &auditor{events: make(chan string, 1)}
	if !a.Record("first") {
		t.Fatal("first Record = false, want true")
	}
	if a.Record("second") {
		t.Fatal("Record on a full queue = true, want false")
	}
}
