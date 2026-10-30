package jobs_test

import (
	"context"
	"strings"
	"testing"

	"acs/internal/jobs"
)

// TestTerminalJobPayloadSecretsRedacted: a job keeps the plain secret it
// must send (SPV passphrase, Download password) only until it finishes.
// Migration 0086's trigger scrubs it on every transition to SUCCESS,
// FAILED or TIMEOUT, whichever code path sets it, so it is not kept for
// the 90-day job retention (handoff plan 2026-10-30 §3a.5).
func TestTerminalJobPayloadSecretsRedacted(t *testing.T) {
	repo, db, deviceID := newLeaseTestDB(t)
	ctx := context.Background()
	payloadOf := func(id string) string {
		t.Helper()
		var p string
		if err := db.QueryRowContext(ctx, `SELECT payload::text FROM jobs WHERE id = $1`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	spv, err := repo.Create(ctx, deviceID, jobs.TypeSetParameter, jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{
		{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: "hunter2hunter2", Type: "xsd:string"},
		{Name: "Device.WiFi.SSID.1.SSID", Value: "SENT-5G", Type: "xsd:string"},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if p := payloadOf(spv.ID); !strings.Contains(p, "hunter2hunter2") {
		t.Fatalf("queued job lost the value it must send: %s", p)
	}
	if err := repo.MarkSuccess(ctx, spv.ID); err != nil {
		t.Fatal(err)
	}
	if p := payloadOf(spv.ID); strings.Contains(p, "hunter2hunter2") || !strings.Contains(p, "SENT-5G") || !strings.Contains(p, "***REDACTED***") {
		t.Fatalf("SUCCESS payload = %s, want passphrase redacted and SSID kept", p)
	}

	dl, err := repo.Create(ctx, deviceID, jobs.TypeFirmwareDownload, jobs.FirmwareDownloadPayload{
		URL: "https://fw.example/img.bin", Username: "fw", Password: "s3cret-download",
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkFailed(ctx, dl.ID, "9010", "download failed"); err != nil {
		t.Fatal(err)
	}
	if p := payloadOf(dl.ID); strings.Contains(p, "s3cret-download") || !strings.Contains(p, "img.bin") {
		t.Fatalf("FAILED payload = %s, want password redacted and URL kept", p)
	}

	// Any path that finishes a job, not only the repository helpers.
	raw, err := repo.Create(ctx, deviceID, jobs.TypeSetParameter, jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{
		{Name: "Device.XMPP.Connection.1.Password", Value: "xmpp-pass-123", Type: "xsd:string"},
	}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET status = 'TIMEOUT', completed_at = now() WHERE id = $1`, raw.ID); err != nil {
		t.Fatal(err)
	}
	if p := payloadOf(raw.ID); strings.Contains(p, "xmpp-pass-123") {
		t.Fatalf("TIMEOUT payload kept the secret: %s", p)
	}
}
