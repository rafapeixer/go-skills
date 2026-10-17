package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	requestTimeout = 8 * time.Second
	maxInFlight    = 256
	maxBodyBytes   = 64 << 10

	maxItems     = 100
	maxSKUBytes  = 255
	defaultLimit = 20
	maxLimit     = 100
)

type api struct {
	logger  *slog.Logger
	store   *store
	pricing *pricingClient
}

func newHandler(logger *slog.Logger, st *store, pc *pricingClient, shuttingDown *atomic.Bool) http.Handler {
	a := &api{logger: logger, store: st, pricing: pc}
	mux := http.NewServeMux()
	addRoutes(mux, a, shuttingDown)
	return limitInFlight(maxInFlight, withTimeout(requestTimeout, mux))
}

// addRoutes is the whole API surface of the service.
func addRoutes(mux *http.ServeMux, a *api, shuttingDown *atomic.Bool) {
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("GET /readyz", handleReadyz(shuttingDown))
	mux.HandleFunc("POST /orders", a.createOrder)
	mux.HandleFunc("GET /orders/{id}", a.getOrder)
	mux.HandleFunc("GET /orders/{id}/quote", a.quoteOrder)
	mux.HandleFunc("GET /customers/{id}/orders", a.listCustomerOrders)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
}

// limitInFlight sheds requests above n concurrent ones with an immediate 503
// instead of queueing them. Health checks are never shed.
func limitInFlight(n int, next http.Handler) http.Handler {
	sem := make(chan struct{}, n)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "server overloaded")
		}
	})
}

func withTimeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz fails once shutdown begins so the load balancer stops routing
// here before the listener closes. It does not ping Postgres: a database blip
// would pull every replica out of rotation at once.
func handleReadyz(shuttingDown *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if shuttingDown.Load() {
			writeError(w, http.StatusServiceUnavailable, "shutting down")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

type createOrderRequest struct {
	CustomerID int64  `json:"customer_id"`
	Items      []Item `json:"items"`
}

func (req createOrderRequest) validate() string {
	if req.CustomerID <= 0 {
		return "customer_id must be greater than 0"
	}
	if len(req.Items) < 1 || len(req.Items) > maxItems {
		return fmt.Sprintf("items must contain between 1 and %d entries", maxItems)
	}
	seen := make(map[string]struct{}, len(req.Items))
	for _, it := range req.Items {
		if it.SKU == "" || len(it.SKU) > maxSKUBytes || strings.ContainsRune(it.SKU, 0) {
			return fmt.Sprintf("sku must be 1 to %d bytes without NUL characters", maxSKUBytes)
		}
		if it.Qty < 1 {
			return "qty must be at least 1"
		}
		if _, dup := seen[it.SKU]; dup {
			return fmt.Sprintf("duplicate sku %q", it.SKU)
		}
		seen[it.SKU] = struct{}{}
	}
	return ""
}

func (a *api) createOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if err := decodeJSON(w, r, &req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	id, err := a.store.createOrder(r.Context(), req.CustomerID, req.Items)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (a *api) getOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	order, err := a.store.getOrder(r.Context(), id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (a *api) listCustomerOrders(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathID(w, r)
	if !ok {
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

	var after *cursor
	if s := q.Get("cursor"); s != "" {
		c, err := decodeCursor(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		after = &c
	}

	orders, next, err := a.store.listCustomerOrders(r.Context(), customerID, limit, after)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	resp := struct {
		Orders     []Order `json:"orders"`
		NextCursor string  `json:"next_cursor"`
	}{Orders: orders}
	if next != nil {
		resp.NextCursor = next.encode()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *api) quoteOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// The database connection is released before calling the pricing
	// service, so a slow pricing service cannot exhaust the pool.
	order, err := a.store.getOrder(r.Context(), id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	total, err := a.pricing.total(r.Context(), order.Items)
	switch {
	case errors.Is(err, errUnknownSKU):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		a.logger.WarnContext(r.Context(), "pricing failed", "order_id", id, "err", err)
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "pricing service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"order_id": id, "total_cents": total})
}

func (a *api) serverError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errPoolExhausted):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "database busy")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusServiceUnavailable, "request timed out")
	default:
		a.logger.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// pathID parses the {id} path value, writing a 400 when it is invalid.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return err
		}
		return errors.New("unexpected data after JSON body")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
