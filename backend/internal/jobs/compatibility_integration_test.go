package jobs_test

import (
	"acs/internal/jobs"
	"context"
	"encoding/json"
	"testing"
)

func TestReadbackSurvivesRepositoryRestartWithoutRepeatingWrite(t *testing.T) {
	repo, db, device := newLeaseTestDB(t)
	ctx := context.Background()
	payload := jobs.SetParameterPayload{Verify: true, Parameters: []jobs.ParameterWrite{{Name: "Device.WiFi.SSID.1.SSID", Value: "test"}}}
	job, err := repo.Create(ctx, device, jobs.TypeSetParameter, payload, "lab")
	if err != nil {
		t.Fatal(err)
	}
	leased, err := repo.Lease(ctx, device)
	if err != nil || leased == nil {
		t.Fatal(err)
	}
	if err := repo.BindFirmware(ctx, leased); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(leased.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if err := repo.BeginReadback(ctx, leased, payload); err != nil {
		t.Fatal(err)
	}
	if err := repo.BeginReadback(ctx, leased, payload); err == nil {
		t.Fatal("duplicate acknowledgement changed state")
	}
	restarted := jobs.NewRepository(db)
	readback, err := restarted.Lease(ctx, device)
	if err != nil || readback.ID != job.ID {
		t.Fatal(readback, err)
	}
	var saved jobs.SetParameterPayload
	if err := json.Unmarshal(readback.Payload, &saved); err != nil || !saved.VerificationPending || !saved.EvidenceBound {
		t.Fatal("phase not durable", saved, err)
	}
	backdateLease(t, db, job.ID)
	if _, err := restarted.RecoverExpiredLeases(ctx); err != nil {
		t.Fatal(err)
	}
	retried, err := restarted.Lease(ctx, device)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(retried.Payload, &saved); err != nil || !saved.VerificationPending {
		t.Fatal("retry reverted to write", saved, err)
	}
}

func TestUSPInstanceJobsAreNotLeasedByCWMP(t *testing.T) {
	repo, _, device := newLeaseTestDB(t)
	ctx := context.Background()
	_, err := repo.Create(ctx, device, jobs.TypeGetParameter, jobs.GetParameterPayload{Instances: true, Paths: []string{"Device.WiFi.SSID."}}, "lab")
	if err != nil {
		t.Fatal(err)
	}
	if job, err := repo.Lease(ctx, device); err != nil || job != nil {
		t.Fatal("CWMP leased USP-only operation", job, err)
	}
	if job, err := repo.LeaseForUSPTypes(ctx, device, []string{jobs.TypeGetParameter}); err != nil || job == nil {
		t.Fatal("USP failed to lease instance operation", job, err)
	}
}
