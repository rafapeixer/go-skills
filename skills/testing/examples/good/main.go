// Command good converts amounts between currencies. Its tests show the shapes
// this skill asks for: table tests, a hand-written fake, synctest for retries,
// a fuzz test, a b.Loop benchmark and an end-to-end test through run.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"
)

type fixedRates map[string]float64

func (f fixedRates) Rate(_ context.Context, currency string) (float64, error) {
	if r, ok := f[currency]; ok {
		return r, nil
	}
	return 0, errors.New("unknown currency")
}

// run owns the whole service, so a test can start it exactly as main does.
func run(ctx context.Context, ln net.Listener) error {
	conv := &Converter{Rates: fixedRates{"EUR": 0.92}, Timeout: 2 * time.Second, Attempts: 3, Backoff: time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /convert", func(w http.ResponseWriter, r *http.Request) {
		cents, err := ParseAmount(r.URL.Query().Get("amount"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := conv.Convert(r.Context(), cents, r.URL.Query().Get("currency"))
		if err != nil {
			http.Error(w, "rate unavailable", http.StatusBadGateway)
			return
		}
		fmt.Fprintln(w, FormatCents(out))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); srv.Shutdown(context.Background()) }()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ln, err := net.Listen("tcp", "127.0.0.1:8080")
	if err == nil {
		err = run(ctx, ln)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
