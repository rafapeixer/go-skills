package main

import (
	"context"
	"sync"
)

// store stands in for a database; ping stands in for pool.Ping.
type store struct {
	mu    sync.Mutex
	items map[string]string
}

func newStore() *store { return &store{items: map[string]string{}} }

func (s *store) ping(ctx context.Context) error { return ctx.Err() }

func (s *store) get(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, ok := s.items[id]
	return name, ok
}

func (s *store) put(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = name
}
