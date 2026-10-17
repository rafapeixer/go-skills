// Command app is the only place that knows about every domain package. It
// builds them, connects them and runs; it holds no business rules.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/rafapeixer/go-skills/skills/project-layout/examples/good/internal/billing"
	"github.com/rafapeixer/go-skills/skills/project-layout/examples/good/internal/orders"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(context.Background(), logger); err != nil {
		logger.Error("run", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	bill := billing.New(10_000)
	ord := orders.New(bill) // *billing.Service satisfies orders' charger at compile time.

	o, err := ord.Place(ctx, "cus_1", 2_500)
	if err != nil {
		return err
	}
	logger.Info("order placed", "order_id", o.ID, "charge_id", o.ChargeID)
	return nil
}
