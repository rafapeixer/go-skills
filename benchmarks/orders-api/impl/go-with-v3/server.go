package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// maxInFlight caps concurrent API requests per replica. A 2 vCPU replica
	// serving ~2,000 req/s at ~50 ms holds ~100 in flight; 256 leaves room
	// for bursts, and the excess is shed with 503 instead of queued.
	maxInFlight    = 256
	requestTimeout = 5 * time.Second
	readyzTimeout  = time.Second
	maxBodyBytes   = 1 << 20

	maxItems     = 100
	maxSKUBytes  = 256
	defaultLimit = 20
	maxLimit     = 100
)

// newHandler builds the whole HTTP surface. Health checks sit outside load
// shedding so probes keep answering under load.
func newHandler(logger *slog.Logger, st *store, pc *pricingClient, shuttingDown *atomic.Bool) http.Handler {
	a := &api{log: logger, store: st, pricing: pc}
	apiMux := http.NewServeMux()
	addRoutes(apiMux, a)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.Handle("GET /readyz", handleReadyz(st, shuttingDown))
	// Shed requests are not logged, so overload does not also flood the logs.
	root.Handle("/", shed(maxInFlight, logErrors(logger, withTimeout(requestTimeout, apiMux))))
	return root
}

// addRoutes is the API surface of the service.
func addRoutes(mux *http.ServeMux, a *api) {
	mux.HandleFunc("POST /orders", a.createOrder)
	mux.HandleFunc("GET /orders/{id}", a.getOrder)
	mux.HandleFunc("GET /orders/{id}/quote", a.quoteOrder)
	mux.HandleFunc("GET /customers/{id}/orders", a.listCustomerOrders)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
}

// handleReadyz is the load balancer target: it fails as soon as shutdown
// begins and while the database is unreachable.
func handleReadyz(st *store, shuttingDown *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shuttingDown.Load() {
			writeError(w, http.StatusServiceUnavailable, "shutting down")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
		defer cancel()
		if err := st.ping(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
}

type api struct {
	log     *slog.Logger
	store   *store
	pricing *pricingClient
}

type createOrderRequest struct {
	CustomerID int64 `json:"customer_id"`
	Items      []struct {
		SKU string `json:"sku"`
		Qty int64  `json:"qty"`
	} `json:"items"`
}

func (a *api) createOrder(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	var req createOrderRequest
	if err := dec.Decode(&req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must be a single JSON object")
		return
	}

	items, msg := validateOrder(req)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	id, err := a.store.createOrder(r.Context(), req.CustomerID, items)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func validateOrder(req createOrderRequest) ([]item, string) {
	if req.CustomerID <= 0 {
		return nil, "customer_id must be a positive integer"
	}
	if len(req.Items) < 1 || len(req.Items) > maxItems {
		return nil, fmt.Sprintf("items must contain 1 to %d entries", maxItems)
	}
	items := make([]item, 0, len(req.Items))
	seen := make(map[string]struct{}, len(req.Items))
	for i, it := range req.Items {
		switch {
		case it.SKU == "":
			return nil, fmt.Sprintf("items[%d].sku is required", i)
		case len(it.SKU) > maxSKUBytes:
			return nil, fmt.Sprintf("items[%d].sku is longer than %d bytes", i, maxSKUBytes)
		case strings.ContainsRune(it.SKU, 0):
			return nil, fmt.Sprintf("items[%d].sku contains a NUL character", i)
		case it.Qty < 1 || it.Qty > math.MaxInt32:
			return nil, fmt.Sprintf("items[%d].qty must be between 1 and %d", i, math.MaxInt32)
		}
		if _, dup := seen[it.SKU]; dup {
			return nil, fmt.Sprintf("duplicate sku %q", it.SKU)
		}
		seen[it.SKU] = struct{}{}
		items = append(items, item{SKU: it.SKU, Qty: int32(it.Qty)})
	}
	return items, ""
}

func (a *api) getOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	o, err := a.store.getOrder(r.Context(), id)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
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

	orders, next, err := a.store.listOrders(r.Context(), customerID, limit, after)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	resp := struct {
		Orders     []order `json:"orders"`
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
	o, err := a.store.getOrder(r.Context(), id)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	total, err := a.pricing.quote(r.Context(), o.Items)
	switch {
	case errors.Is(err, errUnknownSKU):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		a.log.WarnContext(r.Context(), "quote failed", "order_id", id, "err", err)
		writeUnavailable(w, "pricing service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"order_id": id, "total_cents": total})
}

// dbError maps store errors to responses and logs the unexpected ones.
func (a *api) dbError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "order not found")
	case errors.Is(err, errPoolExhausted):
		writeUnavailable(w, "database busy")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeUnavailable(w, "request timed out")
	default:
		a.log.ErrorContext(r.Context(), "database error", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeUnavailable(w http.ResponseWriter, msg string) {
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusServiceUnavailable, msg)
}

// shed caps in-flight requests and rejects the excess at once.
func shed(limit int, next http.Handler) http.Handler {
	slots := make(chan struct{}, limit)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			next.ServeHTTP(w, r)
		default:
			writeUnavailable(w, "server overloaded")
		}
	})
}

// withTimeout gives every request a deadline that database and pricing
// calls derive from.
func withTimeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// logErrors logs 5xx responses; logging every request would cost more CPU
// than the requests on a t4g.micro.
func logErrors(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status >= 500 {
			logger.LogAttrs(r.Context(), slog.LevelWarn, "request failed",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)))
		}
	})
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
