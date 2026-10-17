package main

import (
	"bufio"
	"context"
	"net"
	"sync"
	"time"
)

// fakePrices answers after delay, or stops as soon as ctx ends.
type fakePrices struct{ delay time.Duration }

func (f fakePrices) Price(ctx context.Context, sku string) (int, error) {
	t := time.NewTimer(f.delay)
	defer t.Stop()
	select {
	case <-t.C:
		return len(sku) * 10, nil
	case <-ctx.Done():
		return 0, context.Cause(ctx)
	}
}

// memAudit simulates a write that takes 100ms and is dropped if ctx ends first.
type memAudit struct {
	mu      sync.Mutex
	records [][]string
}

func (a *memAudit) Record(ctx context.Context, skus []string) {
	t := time.NewTimer(100 * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, skus)
}

func (a *memAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.records)
}

// readLine reads from a conn whose Read takes no ctx. AfterFunc turns
// cancellation into an expired deadline, and stop releases it on return,
// so no goroutine is left waiting on ctx.Done().
func readLine(ctx context.Context, conn net.Conn) (string, error) {
	stop := context.AfterFunc(ctx, func() {
		conn.SetReadDeadline(time.Now())
	})
	defer stop()
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && ctx.Err() != nil {
		return "", context.Cause(ctx)
	}
	return line, err
}
