package services

import (
	"context"

	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/internal/models"
	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/internal/repository"
	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/pkg/utils"
)

// MISTAKE: the provider declares the interface with every method it has, so
// each consumer depends on all of them and each fake must implement all of them.
type BillingService interface {
	Charge(ctx context.Context, customerID string, cents int64) (models.Charge, error)
	Refund(ctx context.Context, chargeID string) error
	ListCharges(ctx context.Context, customerID string) ([]models.Charge, error)
}

// MISTAKE: returns the interface; adding a method breaks every fake, and
// callers cannot see the concrete type's documentation or extra methods.
func NewBillingService(store *repository.Store) BillingService {
	return &billingService{store: store}
}

type billingService struct{ store *repository.Store }

func (s *billingService) Charge(ctx context.Context, customerID string, cents int64) (models.Charge, error) {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()
	c := models.Charge{ID: utils.NewID("ch", len(s.store.Charges)+1), CustomerID: customerID, Cents: cents}
	s.store.Charges[c.ID] = c
	return c, nil
}

func (s *billingService) Refund(ctx context.Context, chargeID string) error {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()
	delete(s.store.Charges, chargeID)
	return nil
}

func (s *billingService) ListCharges(ctx context.Context, customerID string) ([]models.Charge, error) {
	return nil, nil
}
