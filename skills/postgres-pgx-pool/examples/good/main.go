package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// 90 of the server's max_connections are for this service; the
	// deployment runs at most 6 pods during a surge, so each pod gets 15.
	cfg, err := newPoolConfig(os.Getenv("DATABASE_URL"), 90, 6)
	if err != nil {
		slog.Error("configure database", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		slog.Error("create database pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// NewWithConfig connects lazily; Ping fails the start when the database is unreachable.
	if err := pool.Ping(ctx); err != nil {
		slog.Error("ping database", "err", err)
		return
	}
	// Serve requests here, running every query through withConn.
	slog.Info("database ready", "max_conns", cfg.MaxConns)
}
