package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	maxBodyBytes = 64 << 10
	maxItems     = 100
	defaultLimit = 20
	maxLimit     = 100
	// Bounds each request's database work, including waiting for a pooled
	// connection, so an overloaded database yields 503s rather than a queue.
	dbTimeout = 3 * time.Second
)

type API struct {
	store    *Store
	pricing  *PricingClient
	log      *slog.Logger
	draining *atomic.Bool
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", a.createOrder)
	mux.HandleFunc("GET /orders/{id}", a.getOrder)
	mux.HandleFunc("GET /orders/{id}/quote", a.quoteOrder)
	mux.HandleFunc("GET /customers/{id}/orders", a.listCustomerOrders)
	mux.HandleFunc("GET /healthz", a.healthz)
	return a.recoverAndLog(mux)
}

func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	if a.draining.Load() {
		writeError(w, http.StatusServiceUnavailable, "shutting down")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type createOrderRequest struct {
	CustomerID int64  `json:"customer_id"`
	Items      []Item `json:"items"`
}

func (a *API) createOrder(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req createOrderRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		writeError(w, http.StatusBadRequest, "unexpected data after JSON body")
		return
	}
	if msg := validateCreate(&req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()
	id, err := a.store.CreateOrder(ctx, req.CustomerID, req.Items)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func validateCreate(req *createOrderRequest) string {
	if req.CustomerID <= 0 {
		return "customer_id must be a positive integer"
	}
	if len(req.Items) < 1 || len(req.Items) > maxItems {
		return "items must contain between 1 and 100 entries"
	}
	seen := make(map[string]struct{}, len(req.Items))
	for _, it := range req.Items {
		if it.SKU == "" {
			return "sku must not be empty"
		}
		if it.Qty < 1 {
			return "qty must be at least 1"
		}
		if _, dup := seen[it.SKU]; dup {
			return "duplicate sku: " + it.SKU
		}
		seen[it.SKU] = struct{}{}
	}
	return ""
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()
	order, err := a.store.GetOrder(ctx, id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (a *API) listCustomerOrders(w http.ResponseWriter, r *http.Request) {
	customerID, ok := parseID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid customer id")
		return
	}
	q := r.URL.Query()

	limit := defaultLimit
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, maxLimit)
	}

	var after *Cursor
	if s := q.Get("cursor"); s != "" {
		c, err := DecodeCursor(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		after = &c
	}

	ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	defer cancel()
	orders, next, err := a.store.ListCustomerOrders(ctx, customerID, after, limit)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	resp := struct {
		Orders     []Order `json:"orders"`
		NextCursor string  `json:"next_cursor"`
	}{Orders: orders}
	if next != nil {
		resp.NextCursor = next.Encode()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) quoteOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}

	// Load the order and release the DB connection before calling pricing, so
	// a slow pricing service cannot exhaust the connection pool.
	dbCtx, cancel := context.WithTimeout(r.Context(), dbTimeout)
	order, err := a.store.GetOrder(dbCtx, id)
	cancel()
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		a.dbError(w, r, err)
		return
	}

	total, err := a.pricing.Quote(r.Context(), order.Items)
	switch {
	case errors.Is(err, errUnknownSKU):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		a.log.Warn("pricing failed", "order_id", id, "err", err)
		writeError(w, http.StatusServiceUnavailable, "pricing service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"order_id": id, "total_cents": total})
}

func (a *API) dbError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		a.log.Warn("database timeout", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	a.log.Error("database error", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (a *API) recoverAndLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				a.log.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", p)
				writeError(rec, http.StatusInternalServerError, "internal error")
			}
			if r.URL.Path != "/healthz" {
				a.log.Info("request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", rec.status,
					"duration_ms", time.Since(start).Milliseconds(),
				)
			}
		}()
		next.ServeHTTP(rec, r)
	})
}
