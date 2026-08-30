// Command migrate applies the embedded schema migrations to
// ACS_POSTGRES_DSN and exits — the same store.Migrate every service runs
// at startup, exposed as a standalone step so CI can prove a clean
// database migrates (and re-migrates idempotently, and survives two
// concurrent runs), and so an operator can migrate ahead of a rolling
// deploy instead of letting the first new replica do it under load.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"acs/internal/store"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	dsn := os.Getenv("ACS_POSTGRES_DSN")
	if dsn == "" {
		logger.Error("ACS_POSTGRES_DSN is required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	db, err := store.Open(ctx, dsn)
	if err != nil {
		logger.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	start := time.Now()
	if err := store.Migrate(ctx, db); err != nil {
		logger.Error("migration failed", "err", err)
		os.Exit(1)
	}
	logger.Info("migrations applied", "took", time.Since(start).Round(time.Millisecond))
}
