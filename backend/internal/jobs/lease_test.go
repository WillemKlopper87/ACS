package jobs_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

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

// TestLeaseForTypesFiltersByType covers LeaseForTypes's type parameter: a
// USP dispatch worker that can only build requests for a subset of job
// types must never lease one outside that subset, even though it's the
// oldest QUEUED job for the device.
func TestLeaseForTypesFiltersByType(t *testing.T) {
	repo, _, deviceID := newLeaseTestDB(t)
	ctx := context.Background()

	upload, err := repo.Create(ctx, deviceID, jobs.TypeUpload,
		jobs.UploadPayload{FileType: "1 Vendor Configuration File", URL: "http://example.com/upload"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	getParam, err := repo.Create(ctx, deviceID, jobs.TypeGetParameter,
		jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo."}}, "test")
	if err != nil {
		t.Fatal(err)
	}

	leased, err := repo.LeaseForTypes(ctx, deviceID, []string{jobs.TypeGetParameter})
	if err != nil {
		t.Fatal(err)
	}
	if leased == nil || leased.ID != getParam.ID {
		t.Fatalf("LeaseForTypes([]string{TypeGetParameter}) = %+v, want the GetParameter job", leased)
	}
	if leased.Status != jobs.StatusRPCSent {
		t.Errorf("leased job status = %s, want RPC_SENT", leased.Status)
	}

	gotUpload, err := repo.ByID(ctx, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotUpload.Status != jobs.StatusQueued {
		t.Errorf("Upload job status = %s, want QUEUED (untouched by a GetParameter-only lease)", gotUpload.Status)
	}
}

// TestDiagnosticsAwaitLifecycle is the C1 regression: an acknowledged
// diagnostic trigger must not be leasable again in the same session; the
// next Inform releases it for exactly one poll, and the reaper times it out
// by deadline rather than by attempts.
func TestDiagnosticsAwaitLifecycle(t *testing.T) {
	repo, db, deviceID := newLeaseTestDB(t)
	ctx := context.Background()

	job, err := repo.CreateWithMaxAttempts(ctx, deviceID, jobs.TypeDiagnosticsPing,
		jobs.DiagnosticsPingPayload{Host: "8.8.8.8", NumberOfRepetitions: 4, Timeout: 5000}, "test", jobs.DiagnosticsMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := repo.Lease(ctx, deviceID)
	if err != nil || leased == nil || leased.ID != job.ID {
		t.Fatalf("lease trigger: %+v %v", leased, err)
	}
	if jobs.DiagnosticsTriggered(leased) {
		t.Fatal("fresh job must render the trigger")
	}
	if ok, err := repo.MarkAwaitingDiagnostics(ctx, leased, jobs.DiagnosticsWindow(leased)); err != nil || !ok {
		t.Fatalf("mark awaiting: %v %v", ok, err)
	}
	// Same session: nothing to lease.
	if again, err := repo.Lease(ctx, deviceID); err != nil || again != nil {
		t.Fatalf("same-session lease returned %+v %v, want nothing", again, err)
	}
	// Next Inform releases it once.
	if n, err := repo.ReleaseAwaitingDiagnostics(ctx, deviceID); err != nil || n != 1 {
		t.Fatalf("release: %d %v", n, err)
	}
	poll, err := repo.Lease(ctx, deviceID)
	if err != nil || poll == nil || !jobs.DiagnosticsTriggered(poll) {
		t.Fatalf("lease poll: %+v %v, want a triggered job", poll, err)
	}
	firstDeadline := *poll.DiagnosticsDeadline
	if ok, err := repo.MarkAwaitingDiagnostics(ctx, poll, time.Hour); err != nil || !ok {
		t.Fatalf("re-park: %v %v", ok, err)
	}
	reloaded, _ := repo.ByID(ctx, job.ID)
	if reloaded.Status != jobs.StatusAwaitingDiagnosticsComplete || !reloaded.DiagnosticsDeadline.Equal(firstDeadline) {
		t.Fatalf("re-park changed state/deadline: %s %v (first %v)", reloaded.Status, reloaded.DiagnosticsDeadline, firstDeadline)
	}

	// Deadline passes without another Inform -> reaper times it out.
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET diagnostics_deadline = now() - interval '1 second' WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	res, err := repo.RecoverExpiredLeases(ctx)
	if err != nil || res.DiagnosticsTimedOut != 1 {
		t.Fatalf("reaper: %+v %v, want one diagnostics timeout", res, err)
	}
	final, _ := repo.ByID(ctx, job.ID)
	if final.Status != jobs.StatusTimeout {
		t.Fatalf("status = %s, want TIMEOUT", final.Status)
	}
}
