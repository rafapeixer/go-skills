package main

import (
	"context"
	"errors"
)

var errBulkheadFull = errors.New("bulkhead full")

// bulkhead caps concurrent calls to one dependency, so a slow dependency
// holds at most size goroutines instead of every request in the process.
type bulkhead struct {
	slots chan struct{}
}

func newBulkhead(size int) *bulkhead {
	return &bulkhead{slots: make(chan struct{}, size)}
}

// do fails fast when every slot is taken; it never waits for one.
func (b *bulkhead) do(ctx context.Context, f func(context.Context) error) error {
	select {
	case b.slots <- struct{}{}:
	default:
		return errBulkheadFull
	}
	defer func() { <-b.slots }()
	return f(ctx)
}
