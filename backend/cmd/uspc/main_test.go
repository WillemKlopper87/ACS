package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/store"
)

// TestResetUspAgentsConnectedClearsStaleRows is a DB-backed test for
// final-review finding 2: a fresh cmd/uspc process must clear any
// connected=true row left over from a prior instance (crash, restart, or
// any other route that left the row stale), not carry it forward forever
// and permanently exempt that device from the liveness reaper.
func TestResetUspAgentsConnectedClearsStaleRows(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Seed a usp_agents row exactly as a prior process's LinkUspAgent
	// would have left it: connected = true.
	repo := devices.NewRepository(db)
	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}.NaturalKey()
	if _, err := repo.PreRegister(ctx, ouiSerial, "Acme", "001122", "Router", "ABC123", nil, nil); err != nil {
		t.Fatalf("pre-register seed device: %v", err)
	}
	d, err := repo.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := repo.LinkUspAgent(ctx, d.ID, "os::001122-ABC123", "WebSocket", nil); err != nil {
		t.Fatalf("seed usp_agents link: %v", err)
	}
	agent, err := repo.GetUspAgentByEndpointID(ctx, "os::001122-ABC123")
	if err != nil {
		t.Fatalf("get seeded agent: %v", err)
	}
	if !agent.Connected {
		t.Fatal("seeded agent.Connected = false, want true (test setup is wrong)")
	}

	if err := resetUspAgentsConnected(ctx, db, slog.Default()); err != nil {
		t.Fatalf("resetUspAgentsConnected: %v", err)
	}

	agent, err = repo.GetUspAgentByEndpointID(ctx, "os::001122-ABC123")
	if err != nil {
		t.Fatalf("get agent after reset: %v", err)
	}
	if agent.Connected {
		t.Error("agent.Connected = true after resetUspAgentsConnected, want false")
	}
}
