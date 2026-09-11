package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"acs/internal/jobs"
)

// TestListenReceivesNotification is the end-to-end check for the whole
// Task 2 contract: it starts a real QueueListener against Postgres,
// inserts a job through the real Repository (so the 0054 trigger fires
// exactly as it would in production), and asserts the device id arrives
// on Notifications(). Because Listen is called with jobs.NotifyChannel
// rather than a hardcoded "acs_jobs_queued" literal, this also proves
// jobs.NotifyChannel matches the migration's literal channel name -- if
// the two ever drift, LISTEN subscribes to the wrong channel and this
// test times out instead of passing.
func TestListenReceivesNotification(t *testing.T) {
	repo, db, deviceID := newLeaseTestDB(t)
	ctx := context.Background()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	listener, err := jobs.Listen(ctx, db, jobs.NotifyChannel, log)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	job, err := repo.Create(ctx, deviceID, jobs.TypeSetParameter,
		jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.X", Value: "1", Type: "xsd:string"}}}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if job.DeviceID != deviceID {
		t.Fatalf("sanity check failed: job.DeviceID = %q, want %q", job.DeviceID, deviceID)
	}

	select {
	case payload := <-listener.Notifications():
		if payload != deviceID {
			t.Errorf("notification payload = %q, want device id %q", payload, deviceID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for notification after job insert")
	}
}

// TestListenCloseStopsCleanly confirms Close stops the listener's
// background goroutine promptly, with no leak and no panic, AND that its
// dedicated connection is actually released back to db's pool -- not
// just that the goroutine exited. Close itself blocks until the
// goroutine has actually exited (it waits on the same internal signal
// the goroutine closes on its way out, which in turn closes
// Notifications()), so asserting Close returns within a short deadline
// -- and that Notifications() is then closed -- covers the goroutine
// half without any test-only production API surface; db.Stats().InUse
// returning to its pre-Listen baseline covers the connection-release
// half (a regression that stopped calling releaseConn/conn.Close() would
// leave InUse permanently elevated by one, even though the goroutine
// still exits cleanly).
func TestListenCloseStopsCleanly(t *testing.T) {
	_, db, _ := newLeaseTestDB(t)
	ctx := context.Background()

	baselineInUse := db.Stats().InUse

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	listener, err := jobs.Listen(ctx, db, jobs.NotifyChannel, log)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	if inUse := db.Stats().InUse; inUse != baselineInUse+1 {
		t.Fatalf("sanity check failed: db.Stats().InUse = %d right after Listen, want %d (baseline+1 for the dedicated listen connection)", inUse, baselineInUse+1)
	}

	closed := make(chan error, 1)
	go func() { closed <- listener.Close() }()

	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close() = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return within 3s; goroutine likely leaked")
	}

	select {
	case _, ok := <-listener.Notifications():
		if ok {
			t.Error("Notifications() yielded a value after Close; want a closed, empty channel")
		}
	default:
		t.Error("Notifications() did not close after Close returned")
	}

	if inUse := db.Stats().InUse; inUse != baselineInUse {
		t.Errorf("db.Stats().InUse = %d after Close, want %d (baseline) -- the dedicated connection was not released back to the pool", inUse, baselineInUse)
	}
}

// TestListenReconnectsAfterConnectionLoss forces the listener's dedicated
// connection to be killed server-side (pg_terminate_backend on its own
// backend PID, found via pg_stat_activity's still-recorded "LISTEN
// acs_jobs_queued" query text -- the listener's connection is idle, but
// Postgres keeps the last executed statement's text visible for an idle
// backend, so this targets only the listener's own session, not any
// other connection on the shared test database), then confirms the
// listener notices, reconnects with its backoff, re-issues LISTEN, and
// still delivers a notification for a job created afterward -- exercising
// the reconnect path end to end rather than only reasoning about it from
// pgx's source.
func TestListenReconnectsAfterConnectionLoss(t *testing.T) {
	repo, db, deviceID := newLeaseTestDB(t)
	ctx := context.Background()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	listener, err := jobs.Listen(ctx, db, jobs.NotifyChannel, log)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	var pid int
	if err := db.QueryRowContext(ctx,
		`SELECT pid FROM pg_stat_activity
		 WHERE query = $1 AND pid <> pg_backend_pid()
		 ORDER BY backend_start DESC LIMIT 1`,
		"LISTEN "+jobs.NotifyChannel,
	).Scan(&pid); err != nil {
		t.Fatalf("find listener's backend pid via pg_stat_activity: %v", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatalf("pg_terminate_backend(%d): %v", pid, err)
	}

	// The killed connection makes WaitForNotification error out, which
	// starts the listener's reconnect-with-backoff (1s initial, doubling,
	// capped at 30s). Poll by creating a job and waiting briefly for its
	// notification, repeating until the listener has resubscribed --
	// avoids this test depending on the exact backoff timing.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("listener never resubscribed and delivered a notification after its connection was killed")
		}
		if _, err := repo.Create(ctx, deviceID, jobs.TypeSetParameter,
			jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.X", Value: "1", Type: "xsd:string"}}}, "test"); err != nil {
			t.Fatalf("create job: %v", err)
		}
		select {
		case payload := <-listener.Notifications():
			if payload != deviceID {
				t.Errorf("notification payload = %q, want %q", payload, deviceID)
			}
			return
		case <-time.After(1 * time.Second):
			// Not resubscribed yet -- try again.
		}
	}
}
