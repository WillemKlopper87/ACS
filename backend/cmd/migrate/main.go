// Command migrate applies the embedded schema migrations to
// ACS_POSTGRES_DSN and exits — the same store.Migrate every service runs
// at startup, exposed as a standalone step so CI can prove a clean
// database migrates (and re-migrates idempotently, and survives two
// concurrent runs), and so an operator can migrate ahead of a rolling
// deploy instead of letting the first new replica do it under load.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"time"

	"acs/internal/store"
	"acs/internal/store/dbroles"
)

func main() {
	check := flag.Bool("check", false, "read-only migration readiness check; never applies migrations")
	flag.Parse()
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
	if *check {
		checks, err := store.CheckMigrations(ctx, db)
		if err != nil {
			logger.Error("migration check failed")
			os.Exit(1)
		}
		ready := true
		for _, item := range checks {
			if item.State != "applied" {
				ready = false
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": ready, "migrations": checks}); err != nil {
			os.Exit(1)
		}
		if !ready {
			os.Exit(1)
		}
		return
	}

	start := time.Now()
	if err := store.Migrate(ctx, db); err != nil {
		logger.Error("migration failed", "err", err)
		os.Exit(1)
	}
	logger.Info("migrations applied", "took", time.Since(start).Round(time.Millisecond))

	// Per-service logins (internal/store/dbroles): set the privileges of
	// the service roles that exist to the committed table. A deployment
	// with one shared login has none of the roles and nothing happens.
	granted, err := dbroles.Apply(ctx, db)
	if err != nil {
		logger.Error("granting service roles failed", "err", err)
		os.Exit(1)
	}
	if len(granted) > 0 {
		logger.Info("service role privileges applied", "roles", granted)
	}
}
