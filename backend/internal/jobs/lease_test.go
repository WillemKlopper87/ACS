package jobs_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/store"
)

func newLeaseTestDB(t *testing.T) (*jobs.Repository, *sql.DB, string) {
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
	devRepo := devices.NewRepository(db)
	dev, err := devRepo.PreRegister(ctx, "LEASE-TEST-01", "TestVendor", "001349", "NR7101", "SER1", nil, nil)
	if err != nil {
		t.Fatalf("pre-register device: %v", err)
	}
	return jobs.NewRepository(db), db, dev.ID
}

// backdateLease forces a leased job's leased_until into the past — the
// tests can't wait out the real 15-minute session lease, so this
// simulates "the lease already expired" directly.
func backdateLease(t *testing.T, db *sql.DB, jobID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE jobs SET leased_until = now() - interval '1 minute' WHERE id = $1`, jobID); err != nil {
		t.Fatal(err)
	}
}

// TestRecoverExpiredLeases_NonRepeatableTypesDeadLetterNotRequeue is the
// P1.7 acceptance gate: a stale AddObject/FactoryReset lease must not be
// blindly re-dispatched — the CPE may have already executed it — while
// an idempotent SET_PARAMETER lease still requeues normally.
func TestRecoverExpiredLeases_NonRepeatableTypesDeadLetterNotRequeue(t *testing.T) {
	repo, db, deviceID := newLeaseTestDB(t)
	ctx := context.Background()

	addObj, err := repo.Create(ctx, deviceID, jobs.TypeAddObject, jobs.AddObjectPayload{ObjectPath: "Device.WiFi.SSID."}, "test")
	if err != nil {
		t.Fatal(err)
	}
	factoryReset, err := repo.Create(ctx, deviceID, jobs.TypeFactoryReset, jobs.FactoryResetPayload{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	setParam, err := repo.Create(ctx, deviceID, jobs.TypeSetParameter,
		jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.X", Value: "1", Type: "xsd:string"}}}, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Lease dispatches the oldest QUEUED session-dispatchable job for the
	// device (ADD_OBJECT/FACTORY_RESET/SET_PARAMETER all qualify — see
	// sessionDispatchableTypes); leasing three times in a row claims all
	// three, oldest first.
	for i := 0; i < 3; i++ {
		if _, err := repo.Lease(ctx, deviceID); err != nil {
			t.Fatal(err)
		}
	}

	backdateLease(t, db, addObj.ID)
	backdateLease(t, db, factoryReset.ID)
	backdateLease(t, db, setParam.ID)

	res, err := repo.RecoverExpiredLeases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeadLetteredUnsafeRetry != 2 {
		t.Errorf("DeadLetteredUnsafeRetry = %d, want 2 (AddObject + FactoryReset)", res.DeadLetteredUnsafeRetry)
	}
	if res.Requeued != 1 {
		t.Errorf("Requeued = %d, want 1 (SetParameter)", res.Requeued)
	}

	gotAdd, _ := repo.ByID(ctx, addObj.ID)
	if gotAdd.Status != jobs.StatusFailed || gotAdd.FaultCode == nil || *gotAdd.FaultCode != "LEASE_EXPIRED_UNSAFE_RETRY" {
		t.Errorf("AddObject after stale lease = status %s fault %v, want FAILED/LEASE_EXPIRED_UNSAFE_RETRY (must not be blindly re-dispatched)", gotAdd.Status, gotAdd.FaultCode)
	}
	gotReset, _ := repo.ByID(ctx, factoryReset.ID)
	if gotReset.Status != jobs.StatusFailed || gotReset.FaultCode == nil || *gotReset.FaultCode != "LEASE_EXPIRED_UNSAFE_RETRY" {
		t.Errorf("FactoryReset after stale lease = status %s fault %v, want FAILED/LEASE_EXPIRED_UNSAFE_RETRY (requires safe reconciliation, not blind retry)", gotReset.Status, gotReset.FaultCode)
	}
	gotSet, _ := repo.ByID(ctx, setParam.ID)
	if gotSet.Status != jobs.StatusQueued {
		t.Errorf("SetParameter after stale lease = status %s, want QUEUED (idempotent RPCs still requeue)", gotSet.Status)
	}
}
