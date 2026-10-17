// Command bad compiles, but every marked line lets work run for clients that
// already gave up, loses the reason for a timeout, or leaks goroutines.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

type prices interface {
	Price(ctx context.Context, sku string) (int, error)
}

type auditLog interface {
	Record(ctx context.Context, skus []string)
}

// MISTAKE: the context is stored in the struct, so every request shares one
// context created at startup; client disconnects and deadlines never reach it.
type server struct {
	ctx    context.Context
	prices prices
	audit  auditLog
}

func newServer(p prices, a auditLog) *server {
	return &server{ctx: context.Background(), prices: p, audit: a}
}

func (s *server) quote(skus []string) (int, error) {
	total := 0
	for _, sku := range skus {
		// MISTAKE: no ctx.Err() check, so the loop keeps calling the upstream
		// for every SKU after the request is already over.
		// MISTAKE: plain WithTimeout and a discarded cancel: the timer lives
		// until it fires, and a timeout says only "context deadline exceeded".
		ctx, _ := context.WithTimeout(s.ctx, 500*time.Millisecond)
		p, err := s.prices.Price(ctx, sku)
		if err != nil {
			return 0, fmt.Errorf("price %s: %w", sku, err)
		}
		total += p
	}
	return total, nil
}

func (s *server) handleQuote(w http.ResponseWriter, r *http.Request) {
	skus := r.URL.Query()["sku"]
	total, err := s.quote(skus)
	if err != nil {
		http.Error(w, "upstream timeout", http.StatusGatewayTimeout)
		return
	}
	fmt.Fprintf(w, "%d\n", total)
	// MISTAKE: r.Context() is cancelled as soon as this handler returns, so the
	// audit write is usually dropped; the goroutine also has no owner to wait on it.
	go s.audit.Record(r.Context(), skus)
}

func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	// MISTAKE: Background() drops the trace ID and ignores the client hanging
	// up; the report keeps running at full cost for nobody.
	s.audit.Record(context.Background(), []string{"report"})
}

func readLine(ctx context.Context, conn net.Conn) (string, error) {
	// MISTAKE: if ctx is never cancelled (Background, or a request that ends
	// normally), this goroutine blocks forever and leaks; use context.AfterFunc.
	go func() {
		<-ctx.Done()
		conn.SetReadDeadline(time.Now())
	}()
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	return string(buf[:n]), err
}

func main() {
	s := newServer(nil, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /quote", s.handleQuote)
	mux.HandleFunc("GET /report", s.handleReport)
	// MISTAKE: no request deadline anywhere. WriteTimeout would not replace it:
	// it only makes late writes fail and never cancels r.Context().
	log.Fatal(http.ListenAndServe(":8080", mux))
}
