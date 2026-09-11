package devices

import (
	"context"
	"os"
	"testing"
	"time"

	"acs/internal/cwmp"
	"acs/internal/store"
)

// newDevicesTestRepo mirrors the DSN-skip harness used throughout the
// backend's other DB-backed test suites (e.g. internal/bss/mapping_test.go's
// newMappingTestRepo): a clean, fully migrated schema per test, skipped
// entirely when no live Postgres is configured for tests.
func newDevicesTestRepo(t *testing.T) (context.Context, *Repository) {
	t.Helper()
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
		t.Fatal(err)
	}
	return ctx, NewRepository(db)
}

// TestUpsertFromInformSetsCWMPManagementProtocol covers the extension made
// to UpsertFromInform alongside Task 3's UpsertFromOnBoard: a device that
// has only ever Informed over CWMP must have 'CWMP' recorded in
// management_protocols (added by migration 0053), both on first insert and
// idempotently across repeated Informs from the same identity.
func TestUpsertFromInformSetsCWMPManagementProtocol(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	id := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}

	d1, err := r.UpsertFromInform(ctx, id, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatalf("first UpsertFromInform: %v", err)
	}
	protocols := managementProtocols(t, ctx, r, d1.ID)
	if !containsAll(protocols, "CWMP") {
		t.Errorf("management_protocols after first Inform = %v, want to contain CWMP", protocols)
	}

	d2, err := r.UpsertFromInform(ctx, id, []string{"2 PERIODIC"})
	if err != nil {
		t.Fatalf("second UpsertFromInform: %v", err)
	}
	if d2.ID != d1.ID {
		t.Fatalf("second Inform created a new row: %s != %s", d2.ID, d1.ID)
	}
	protocols = managementProtocols(t, ctx, r, d1.ID)
	if len(protocols) != 1 || protocols[0] != "CWMP" {
		t.Errorf("management_protocols after repeated CWMP Inform = %v, want exactly [CWMP] (no duplicates)", protocols)
	}
}

// managementProtocols reads the column directly — it is not part of the
// Device struct (a scan-shape decision out of scope for this task), so
// tests that need to assert on it query it themselves.
func managementProtocols(t *testing.T, ctx context.Context, r *Repository, deviceID string) []string {
	t.Helper()
	var protocols store.StringArray
	if err := r.db.QueryRowContext(ctx, `SELECT management_protocols FROM devices WHERE id = $1`, deviceID).Scan(&protocols); err != nil {
		t.Fatalf("read management_protocols: %v", err)
	}
	return []string(protocols)
}

// TestRefreshLivenessSkipsConnectedUSPAgent covers the reaper's new
// exclusion: MTP connection state is authoritative and immediate for USP
// (spec §5.4), so a device with a connected usp_agents row must never be
// moved to UNREACHABLE (or OFFLINE) by the Inform-based inference, no
// matter how stale last_inform_at is — a USP agent that never Informs in
// the first place is not "stale", it's simply not measured that way.
func TestRefreshLivenessSkipsConnectedUSPAgent(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	d, err := r.UpsertFromOnBoard(ctx, "001349", "NR7101", "USP-CONNECTED-01")
	if err != nil {
		t.Fatalf("UpsertFromOnBoard: %v", err)
	}
	if err := r.LinkUspAgent(ctx, d.ID, "endpoint-connected-01", "WebSocket", nil); err != nil {
		t.Fatalf("LinkUspAgent: %v", err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE devices SET online_status = 'ONLINE', last_inform_at = now() - interval '3 hours' WHERE id = $1`, d.ID); err != nil {
		t.Fatalf("set stale last_inform_at: %v", err)
	}

	if _, _, err := r.RefreshLiveness(ctx, 5*time.Minute, 90*time.Minute); err != nil {
		t.Fatalf("RefreshLiveness: %v", err)
	}

	got, err := r.Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OnlineStatus != "ONLINE" {
		t.Errorf("online_status = %q, want ONLINE (connected USP agent must be exempt from Inform-based liveness)", got.OnlineStatus)
	}
}

// TestRefreshLivenessMarksDisconnectedUSPAgentUnreachable covers the other
// side of the contract: once a USP agent's MTP connection drops
// (usp_agents.connected = false), its liveness is genuinely unknown the
// same way a CWMP device's is, so today's last_inform_at-based logic
// applies exactly as before.
func TestRefreshLivenessMarksDisconnectedUSPAgentUnreachable(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	d, err := r.UpsertFromOnBoard(ctx, "001349", "NR7101", "USP-DISCONNECTED-01")
	if err != nil {
		t.Fatalf("UpsertFromOnBoard: %v", err)
	}
	if err := r.LinkUspAgent(ctx, d.ID, "endpoint-disconnected-01", "WebSocket", nil); err != nil {
		t.Fatalf("LinkUspAgent: %v", err)
	}
	if err := r.MarkUspAgentDisconnected(ctx, d.ID, "endpoint-disconnected-01"); err != nil {
		t.Fatalf("MarkUspAgentDisconnected: %v", err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE devices SET online_status = 'ONLINE', last_inform_at = now() - interval '3 hours' WHERE id = $1`, d.ID); err != nil {
		t.Fatalf("set stale last_inform_at: %v", err)
	}

	if _, _, err := r.RefreshLiveness(ctx, 5*time.Minute, 90*time.Minute); err != nil {
		t.Fatalf("RefreshLiveness: %v", err)
	}

	got, err := r.Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OnlineStatus != "UNREACHABLE" {
		t.Errorf("online_status = %q, want UNREACHABLE (disconnected USP agent falls back to last_inform_at-based logic)", got.OnlineStatus)
	}
}

func containsAll(haystack []string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
