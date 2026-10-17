// Command app compiles, but the layout under it couples every feature to
// every layer. Each marked line is a mistake.
package main

import (
	"context"
	"log"

	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/internal/repository"
	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/internal/services"
	"github.com/rafapeixer/go-skills/skills/project-layout/examples/bad/pkg/utils"
)

func main() {
	store := repository.NewStore()
	billing := services.NewBillingService(store)
	orders := &services.OrderService{Billing: billing, Store: store}

	cents := int64(2_500)
	// MISTAKE: a business rule lives in main, where no test reaches it and
	// the next binary (a worker, a CLI) has to copy it.
	if cents > 2_000 {
		cents -= cents / 10
	}
	o, err := orders.Place(context.Background(), "cus_1", cents)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("order %s charged %s", o.ID, utils.FormatCents(o.Cents))
}
