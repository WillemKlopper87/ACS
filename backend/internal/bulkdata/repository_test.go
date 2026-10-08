package bulkdata

import (
	"acs/internal/alerting"
	"acs/internal/jobs"
	"acs/internal/store"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestReceiverRenewalQueuesCredentialsAtomically(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	const device = "11111111-1111-1111-1111-111111111111"
	if _, err := db.ExecContext(ctx, `INSERT INTO devices(id,oui_serial) VALUES($1,'LAB-RECEIVER')`, device); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	receiver, err := repo.Create(ctx, device, 1, []string{"Device.DeviceInfo.SoftwareVersion"}, time.Now().Add(time.Hour), 60)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(7 * 24 * time.Hour)
	payload := jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.BulkData.Profile.1.HTTP.Password", Value: "new-token"}}}
	key, err := repo.RenewAndQueue(ctx, receiver, expiry, payload, "lab")
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.NewRepository(db).ByCommandKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var decoded jobs.SetParameterPayload
	if json.Unmarshal(job.Payload, &decoded) != nil || decoded.Parameters[0].Value != "new-token" {
		t.Fatal("renewal job corrupted")
	}
	current, err := repo.Get(ctx, receiver.ID)
	if err != nil || current.ExpiresAt.Sub(expiry) > time.Second {
		t.Fatal(current, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO account_device_mappings(id,account_id,device_id,oui_serial,role,assigned_at) VALUES(gen_random_uuid(),'lab-account',$1,'LAB-RECEIVER','gateway',now())`, device); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE bulk_data_receivers SET renewed_at=now()-interval '1 hour' WHERE id=$1`, receiver.ID); err != nil {
		t.Fatal(err)
	}
	alerts := alerting.NewRepository(db)
	missing, err := alerts.MissingBulkData(ctx, 10)
	if err != nil || len(missing) != 1 || missing[0].TenantID != "lab-account" {
		t.Fatal("missing report not associated with account", missing, err)
	}
	incidents := alerting.NewIncidentRepository(db)
	if _, err := incidents.Open(ctx, "lab-account", device, "bulk-data:"+receiver.ID, alerting.Priority("P3"), "missing report", nil, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if recovered, err := incidents.RecoverBulkData(ctx); err != nil || len(recovered) != 0 {
		t.Fatal("missing receiver recovered prematurely", recovered, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE bulk_data_receivers SET last_report_at=now() WHERE id=$1`, receiver.ID); err != nil {
		t.Fatal(err)
	}
	if missing, err := alerts.MissingBulkData(ctx, 10); err != nil || len(missing) != 0 {
		t.Fatal("fresh report still missing", missing, err)
	}
	if recovered, err := incidents.RecoverBulkData(ctx); err != nil || len(recovered) != 1 || recovered[0].State != alerting.IncidentRecovered {
		t.Fatal("report did not recover incident", recovered, err)
	}
	if err := repo.Revoke(ctx, receiver.ID, device); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RenewAndQueue(ctx, receiver, expiry, payload, "lab"); err == nil {
		t.Fatal("revoked receiver renewed")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed renewal created orphan job", count, err)
	}
}
