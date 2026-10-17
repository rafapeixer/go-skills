// Package orders places orders. It declares the one billing method it calls
// and does not import package billing; cmd/app connects the two.
package orders

import (
	"context"
	"fmt"
	"sync"
)

// charger is what orders needs from billing, declared here by the consumer.
type charger interface {
	Charge(ctx context.Context, customerID string, cents int64) (string, error)
}

// Order is owned by this package; no shared models package holds it.
type Order struct {
	ID         string
	CustomerID string
	ChargeID   string
	Cents      int64
}

// Service is the orders domain.
type Service struct {
	charger charger
	mu      sync.Mutex
	orders  map[string]Order
}

// New returns a concrete *Service, not an interface.
func New(c charger) *Service {
	return &Service{charger: c, orders: make(map[string]Order)}
}

// Place charges the customer and stores the order.
func (s *Service) Place(ctx context.Context, customerID string, cents int64) (Order, error) {
	chargeID, err := s.charger.Charge(ctx, customerID, cents)
	if err != nil {
		return Order{}, fmt.Errorf("place order for %s: %w", customerID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := Order{
		ID:         fmt.Sprintf("ord_%d", len(s.orders)+1),
		CustomerID: customerID,
		ChargeID:   chargeID,
		Cents:      cents,
	}
	s.orders[o.ID] = o
	return o, nil
}
