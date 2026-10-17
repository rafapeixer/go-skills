// MISTAKE: a layer package stores every domain's data behind exported maps,
// so any package can read or write billing's charges directly.
package repository

import (
	"sync"

	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/internal/models"
)

type Store struct {
	Mu      sync.Mutex
	Orders  map[string]models.Order
	Charges map[string]models.Charge
}

func NewStore() *Store {
	return &Store{Orders: map[string]models.Order{}, Charges: map[string]models.Charge{}}
}
