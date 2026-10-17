// Package billing charges and refunds customers. It owns its types and its
// storage; no other package reads its maps.
package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrDeclined is returned when a charge exceeds the customer's limit.
var ErrDeclined = errors.New("payment declined")

// Service is the billing domain. Callers get the concrete type and choose
// which of its methods they depend on.
type Service struct {
	mu      sync.Mutex
	limit   int64
	charges map[string]int64 // charge ID -> amount in cents
}

// New returns a Service that declines charges above limitCents.
func New(limitCents int64) *Service {
	return &Service{limit: limitCents, charges: make(map[string]int64)}
}

// Charge records a charge and returns its ID.
func (s *Service) Charge(ctx context.Context, customerID string, cents int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cents > s.limit {
		return "", fmt.Errorf("charge customer %s: %w", customerID, ErrDeclined)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := fmt.Sprintf("ch_%d", len(s.charges)+1)
	s.charges[id] = cents
	return id, nil
}

// Refund removes a charge.
func (s *Service) Refund(ctx context.Context, chargeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.charges, chargeID)
	return nil
}
