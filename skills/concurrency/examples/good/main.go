// Command good fans out lookups with a bounded errgroup, guards shared state
// with a mutex, and hands audit events to a goroutine whose owner stops it
// and waits for it before exiting.
package main

import (
	"context"
	"log/slog"
	"os"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(context.Background(), logger); err != nil {
		logger.Error("run", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	c := &cache{m: map[string]Price{}}
	a := startAuditor(logger, 64)
	defer a.Stop() // run does not return while the auditor is still running

	ps, err := prices(ctx, []string{"pen", "ink", "paper", "desk", "lamp"}, c.lookup)
	if err != nil {
		return err
	}
	for _, p := range ps {
		a.Record(p.SKU)
	}
	logger.Info("priced", "count", len(ps))
	return nil
}
