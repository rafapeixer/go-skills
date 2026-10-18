package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// maxInFlight caps concurrent API requests per replica; the excess gets an
	// immediate 503. The database pool and pricing bulkhead shed earlier; this
	// bounds memory and goroutines on a 1 GiB instance. Tune from a load test.
	maxInFlight = 512

	requestTimeout = 10 * time.Second // below the server's 15 s WriteTimeout
	quoteTimeout   = 3 * time.Second

	maxBodyBytes    = 64 << 10 // 100 items with 256-byte SKUs fit comfortably
	maxItems        = 100
	maxSKULen       = 256
	defaultPageSize = 20
	maxPageSize     = 100
)

type app struct {
	store   *store
	pricing *pricingClient
	logger  *slog.Logger
}

func newHandler(a *app, pool *pgxpool.Pool, shuttingDown *atomic.Bool) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("POST /orders", a.handleCreateOrder)
	api.HandleFunc("GET /orders/{id}", a.handleGetOrder)
	api.HandleFunc("GET /orders/{id}/quote", a.handleQuote)
	api.HandleFunc("GET /customers/{id}/orders", a.handleListOrders)

	// Probes sit outside load shedding so they keep answering under load.
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.Handle("GET /readyz", handleReadyz(pool, shuttingDown))
	root.Handle("/", shed(make(limiter, maxInFlight), withTimeout(requestTimeout, api)))
	return root
}

// handleReadyz fails once shutdown starts or the database is unreachable,
// taking the replica out of rotation without restarting it.
func handleReadyz(pool *pgxpool.Pool, shuttingDown *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shuttingDown.Load() {
			writeError(w, http.StatusServiceUnavailable, "shutting down")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
}

func shed(l limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.tryAcquire() {
			unavailable(w, "server overloaded")
			return
		}
		defer l.release()
		next.ServeHTTP(w, r)
	})
}

func withTimeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type createOrderRequest struct {
	CustomerID int64  `json:"customer_id"`
	Items      []item `json:"items"`
}

func (a *app) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
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
	if msg := validateCreateOrder(req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	id, err := a.store.CreateOrder(r.Context(), req.CustomerID, req.Items)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func validateCreateOrder(req createOrderRequest) string {
	if req.CustomerID <= 0 {
		return "customer_id must be a positive integer"
	}
	if len(req.Items) < 1 || len(req.Items) > maxItems {
		return "items must contain between 1 and 100 entries"
	}
	seen := make(map[string]struct{}, len(req.Items))
	for _, it := range req.Items {
		if !validSKU(it.SKU) {
			return "each item needs a sku of 1 to 256 printable characters"
		}
		if it.Qty < 1 || it.Qty > math.MaxInt32 {
			return "qty must be a positive integer"
		}
		if _, dup := seen[it.SKU]; dup {
			return "duplicate sku: " + it.SKU
		}
		seen[it.SKU] = struct{}{}
	}
	return ""
}

func validSKU(s string) bool {
	if s == "" || len(s) > maxSKULen || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) { // includes NUL, which Postgres text rejects
			return false
		}
	}
	return true
}

func (a *app) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}
	o, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

type listOrdersResponse struct {
	Orders     []order `json:"orders"`
	NextCursor string  `json:"next_cursor"`
}

func (a *app) handleListOrders(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid customer id")
		return
	}
	q := r.URL.Query()
	limit := defaultPageSize
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, maxPageSize)
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

	orders, next, err := a.store.ListOrders(r.Context(), customerID, limit, after)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	resp := listOrdersResponse{Orders: orders}
	if next != nil {
		resp.NextCursor = next.encode()
	}
	writeJSON(w, http.StatusOK, resp)
}

type quoteResponse struct {
	OrderID    int64 `json:"order_id"`
	TotalCents int64 `json:"total_cents"`
}

func (a *app) handleQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}
	// The connection is released before any pricing call is made.
	o, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), quoteTimeout)
	defer cancel()
	total, err := a.pricing.Total(ctx, o.Items)
	switch {
	case errors.Is(err, errUnknownSKU):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		a.logger.WarnContext(r.Context(), "quote failed", "order_id", id, "err", err)
		unavailable(w, "pricing service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, quoteResponse{OrderID: o.ID, TotalCents: total})
}

// fail maps a store error to a response; only unexpected errors are logged.
func (a *app) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "order not found")
	case errors.Is(err, errPoolExhausted):
		unavailable(w, "database busy")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), isQueryCanceled(err):
		unavailable(w, "request timed out")
	default:
		a.logger.ErrorContext(r.Context(), "request failed",
			"method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// isQueryCanceled reports a statement cancelled by statement_timeout (57014).
func isQueryCanceled(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "57014"
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func unavailable(w http.ResponseWriter, msg string) {
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusServiceUnavailable, msg)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
