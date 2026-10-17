package services

import (
	"context"
	"fmt"

	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/internal/models"
	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/internal/repository"
	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/pkg/utils"
)

type OrderService struct {
	Billing BillingService
	Store   *repository.Store
}

func (s *OrderService) Place(ctx context.Context, customerID string, cents int64) (models.Order, error) {
	c, err := s.Billing.Charge(ctx, customerID, cents)
	if err != nil {
		return models.Order{}, fmt.Errorf("place order for %s: %w", customerID, err)
	}
	s.Store.Mu.Lock()
	defer s.Store.Mu.Unlock()
	// MISTAKE: orders reads billing's storage directly because they share a
	// layer package and a store; billing can no longer change how it keeps charges.
	if _, ok := s.Store.Charges[c.ID]; !ok {
		return models.Order{}, fmt.Errorf("place order for %s: charge %s missing", customerID, c.ID)
	}
	o := models.Order{ID: utils.NewID("ord", len(s.Store.Orders)+1), CustomerID: customerID, ChargeID: c.ID, Cents: cents}
	s.Store.Orders[o.ID] = o
	return o, nil
}
