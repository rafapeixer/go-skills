package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// startSlow serves one handler that blocks until release is closed, sends a
// request to it, and returns once that request is in flight.
func startSlow(t *testing.T, budget time.Duration, release chan struct{}) (cancel func(), status <-chan int, done <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release // a request still running when SIGTERM arrives
	})}
	ctx, cancel := context.WithCancel(t.Context())
	d := make(chan error, 1)
	go func() { d <- serve(ctx, srv, ln, budget) }()

	s := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			s <- 0
			return
		}
		resp.Body.Close()
		s <- resp.StatusCode
	}()
	<-started
	return cancel, s, d
}

func TestShutdownDrainsInFlightRequest(t *testing.T) {
	release := make(chan struct{})
	cancel, status, done := startSlow(t, 2*time.Second, release)
	cancel() // what SIGTERM does through signal.NotifyContext
	time.AfterFunc(50*time.Millisecond, func() { close(release) })

	if got := <-status; got != http.StatusOK {
		t.Errorf("in-flight request got %d, want 200", got)
	}
	if err := <-done; err != nil {
		t.Errorf("serve = %v, want nil", err)
	}
}

func TestShutdownGivesUpAtBudget(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	cancel, status, done := startSlow(t, 100*time.Millisecond, release)
	begin := time.Now()
	cancel()

	err := <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("serve = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Errorf("serve returned after %v; a stuck request must not outlive the budget", d)
	}
	<-status // Close cut the stuck request off
}
