package jobs_test

import (
	"context"
	"errors"
	"testing"

	"acs/internal/jobs"
	"acs/internal/parameters"
)

// TestProtectedWritesRefusedAtJobCreation: every parameter write is a
// SET_PARAMETER job, so refusing protected parameters here covers the API,
// templates, policies, schedules, bulk actions and BSS orders alike. Only
// flows that own such a parameter (credential rotation, XMPP provisioning)
// may write it, and only by marking their context.
func TestProtectedWritesRefusedAtJobCreation(t *testing.T) {
	repo, db, deviceID := newLeaseTestDB(t)
	ctx := context.Background()
	jobCount := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE device_id = $1`, deviceID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	acsURL := jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{
		{Name: "Device.WiFi.SSID.1.SSID", Value: "SENT-5G", Type: "xsd:string"},
		{Name: "Device.ManagementServer.URL", Value: "http://evil.example/acs", Type: "xsd:string"},
	}}

	_, err := repo.Create(ctx, deviceID, jobs.TypeSetParameter, acsURL, "noc-1")
	var pe *parameters.ProtectedParameterError
	if !errors.As(err, &pe) || pe.Name != "Device.ManagementServer.URL" {
		t.Fatalf("Create err = %v, want a protected-parameter refusal for the ACS URL", err)
	}
	if _, err := repo.CreateWithIdempotency(ctx, deviceID, jobs.TypeSetParameter, acsURL, "bss", "order-1"); !errors.Is(err, parameters.ErrProtectedParameter) {
		t.Fatalf("CreateWithIdempotency err = %v, want refusal", err)
	}
	tr69Off := jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.IP.Interface.2.X_ALU-COM_WanAccessCfg.Tr69Disabled", Value: "1", Type: "xsd:boolean"}}}
	if _, err := repo.CreateWithMaxAttempts(ctx, deviceID, jobs.TypeSetParameter, tr69Off, "template:x", 3); !errors.Is(err, parameters.ErrProtectedParameter) {
		t.Fatalf("CreateWithMaxAttempts err = %v, want refusal", err)
	}
	if n := jobCount(); n != 0 {
		t.Fatalf("%d jobs created for refused writes", n)
	}

	// Reading a protected parameter is fine.
	if _, err := repo.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: []string{"Device.ManagementServer.URL"}}, "noc-1"); err != nil {
		t.Fatalf("read refused: %v", err)
	}
	// Ordinary writes are unaffected.
	if _, err := repo.Create(ctx, deviceID, jobs.TypeSetParameter, jobs.SetParameterPayload{Parameters: acsURL.Parameters[:1]}, "noc-1"); err != nil {
		t.Fatalf("SSID write refused: %v", err)
	}
	// A flow that owns the parameter marks its context.
	rotation := jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{
		{Name: "Device.ManagementServer.ConnectionRequestUsername", Value: "cr-1", Type: "xsd:string"},
		{Name: "Device.ManagementServer.ConnectionRequestPassword", Value: "s3cret", Type: "xsd:string"},
	}}
	if _, err := repo.Create(jobs.AllowProtectedWrites(ctx, "credential rotation"), deviceID, jobs.TypeSetParameter, rotation, "noc-1"); err != nil {
		t.Fatalf("credential rotation refused: %v", err)
	}
	if n := jobCount(); n != 3 {
		t.Fatalf("jobs = %d, want the read, the SSID write and the rotation", n)
	}
}
