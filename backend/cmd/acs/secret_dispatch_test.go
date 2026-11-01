package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"acs/internal/jobs"
	"acs/internal/secretstore"
	"acs/internal/store"
)

// TestOpenSecretHandles: a SET_PARAMETER job naming a sealed handle on two
// bands renders the value on both, opens the handle once, leaves the job's
// own payload with the handle only, and a second dispatch (a retry) fails.
func TestOpenSecretHandles(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.OpenUTC(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	secrets, err := secretstore.New(db, "acs-dispatch-secret-test-key-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	const device = "55555555-5555-5555-5555-555555555555"
	handle, err := secrets.Put(ctx, "", "dispatch-passphrase", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Claim(ctx, handle.ID, "", device); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(jobs.SetParameterPayload{Verify: true, Parameters: []jobs.ParameterWrite{
		{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Type: "xsd:string", SecretHandle: handle.ID},
		{Name: "Device.WiFi.AccessPoint.2.Security.KeyPassphrase", Type: "xsd:string", SecretHandle: handle.ID},
		{Name: "Device.WiFi.SSID.1.SSID", Value: "Home", Type: "xsd:string"},
	}})
	job := &jobs.Job{ID: "job-1", DeviceID: device, Type: jobs.TypeSetParameter, CommandKey: "ck-1", Payload: payload}
	h := &handler{secrets: secrets}

	rendered, err := h.openSecretHandles(ctx, job)
	if err != nil {
		t.Fatalf("openSecretHandles: %v", err)
	}
	body, ok := h.renderJobRequest(rendered)
	if !ok || strings.Count(string(body), "dispatch-passphrase") != 2 || strings.Contains(string(body), handle.ID) {
		t.Fatalf("rendered RPC: ok=%v %s", ok, body)
	}
	if strings.Contains(string(job.Payload), "dispatch-passphrase") || !strings.Contains(string(job.Payload), handle.ID) {
		t.Fatalf("the job's own payload changed: %s", job.Payload)
	}
	if _, err := h.openSecretHandles(ctx, job); !errors.Is(err, secretstore.ErrHandleGone) {
		t.Fatalf("second dispatch: %v, want ErrHandleGone", err)
	}

	// A verification read-back renders a GPV and opens nothing.
	var verifying jobs.SetParameterPayload
	_ = json.Unmarshal(payload, &verifying)
	verifying.VerificationPending = true
	raw, _ := json.Marshal(verifying)
	if got, err := h.openSecretHandles(ctx, &jobs.Job{Type: jobs.TypeSetParameter, DeviceID: device, Payload: raw}); err != nil || string(got.Payload) != string(raw) {
		t.Fatalf("read-back: %v", err)
	}
	// No store: a job with a handle fails instead of sending an empty value.
	if _, err := (&handler{}).openSecretHandles(ctx, job); !errors.Is(err, secretstore.ErrDisabled) {
		t.Fatalf("no store: %v, want ErrDisabled", err)
	}
}
