package main

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// limiter caps in-flight requests. The cap moves by AIMD: it grows by about
// one per window of fast requests and drops by 10% after a slow or failed one.
type limiter struct {
	mu          sync.Mutex
	limit       float64
	floor, ceil float64
	inFlight    int
	target      time.Duration // latency above which the server counts as overloaded
}

func newLimiter(initial, floor, ceil int, target time.Duration) *limiter {
	return &limiter{limit: float64(initial), floor: float64(floor), ceil: float64(ceil), target: target}
}

// tryAcquire never waits: a request either gets a slot now or is shed.
func (l *limiter) tryAcquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight >= int(l.limit) {
		return false
	}
	l.inFlight++
	return true
}

func (l *limiter) release(latency time.Duration, failed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight--
	if failed || latency > l.target {
		l.limit = max(l.floor, l.limit*0.9) // multiplicative decrease
	} else {
		l.limit = min(l.ceil, l.limit+1/l.limit) // additive increase
	}
}

func (l *limiter) current() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int(l.limit)
}

// shed answers 503 with Retry-After at once when the limiter is full,
// instead of parking the request in a queue that grows with the overload.
func shed(l *limiter, retryAfter time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.tryAcquire() {
			overloaded(w, retryAfter)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() { l.release(time.Since(start), rec.status >= 500) }()
		next.ServeHTTP(rec, r)
	})
}

// overloaded tells the client to back off and when to come back.
func overloaded(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
	http.Error(w, "overloaded, retry later", http.StatusServiceUnavailable)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.NewResponseController reach the real writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
