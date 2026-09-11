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
// background goroutine promptly, with no leak and no panic. Close itself
// blocks until the goroutine has actually exited (it waits on the same
// internal signal the goroutine closes on its way out, which in turn
// closes Notifications()), so asserting Close returns within a short
// deadline -- and that Notifications() is then closed -- is sufficient
// without any test-only production API surface.
func TestListenCloseStopsCleanly(t *testing.T) {
	_, db, _ := newLeaseTestDB(t)
	ctx := context.Background()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	listener, err := jobs.Listen(ctx, db, jobs.NotifyChannel, log)
	if err != nil {
		t.Fatalf("Listen: %v", err)
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
}
