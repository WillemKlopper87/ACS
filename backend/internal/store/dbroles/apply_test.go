package dbroles

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"acs/internal/retention"
	"acs/internal/store"
)

// rolesDB migrates the test database and gives it the four service roles
// with the manifest's privileges. The roles cannot log in: the tests
// switch to them with SET ROLE, which is refused or allowed by the same
// privilege checks a real login would meet.
func rolesDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed role test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, svc := range Services {
		if _, err := db.ExecContext(ctx, `DO $$ BEGIN CREATE ROLE `+svc.Role+` NOLOGIN; EXCEPTION WHEN duplicate_object THEN NULL; END $$`); err != nil {
			t.Fatalf("create role %s: %v", svc.Role, err)
		}
	}
	granted, err := Apply(ctx, db)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(granted) != len(Services) {
		t.Fatalf("granted %v, want all %d roles", granted, len(Services))
	}
	return ctx, db
}

// as runs one statement as a service role, in a transaction that is
// always rolled back, and returns its error.
func as(ctx context.Context, t *testing.T, db *sql.DB, role, stmt string, args ...any) error {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE `+role); err != nil {
		t.Fatalf("set role %s: %v", role, err)
	}
	_, err = tx.ExecContext(ctx, stmt, args...)
	return err
}

func denied(err error) bool {
	return err != nil && strings.Contains(err.Error(), "permission denied")
}

func TestRolesAreRefusedWhatTheyDoNotNeed(t *testing.T) {
	ctx, db := rolesDB(t)

	// The point of the exercise: the internet-facing adapter cannot read
	// operator accounts, sign-in secrets or device credentials.
	refused := []struct{ role, stmt string }{
		{"acs_bss", `SELECT * FROM operators`},
		{"acs_bss", `SELECT * FROM operator_mfa`},
		{"acs_bss", `SELECT * FROM operator_mfa_recovery_codes`},
		{"acs_bss", `SELECT * FROM password_reset_tokens`},
		{"acs_bss", `SELECT * FROM device_credentials`},
		{"acs_bss", `UPDATE role_permissions SET role = role`},
		{"acs_bss", `DELETE FROM audit_log`},
		// The gateway and the USP controller face devices: no integration
		// secrets, no operator accounts.
		{"acs_gateway", `SELECT * FROM bss_oauth_clients`},
		{"acs_gateway", `SELECT * FROM webhook_subscriptions`},
		{"acs_gateway", `SELECT * FROM operators`},
		{"acs_gateway", `SELECT * FROM operator_mfa`},
		{"acs_usp", `SELECT * FROM bss_oauth_clients`},
		{"acs_usp", `SELECT * FROM webhook_subscriptions`},
		{"acs_usp", `SELECT * FROM operators`},
		// An event of the caller's choosing cannot be written through the
		// trigger's helper.
		{"acs_gateway", `SELECT emit_device_event((SELECT d FROM devices d LIMIT 1), 'DeviceSeen', '{}'::jsonb)`},
	}
	for _, c := range refused {
		if err := as(ctx, t, db, c.role, c.stmt); !denied(err) {
			t.Errorf("%s: %q should be refused, got %v", c.role, c.stmt, err)
		}
	}

	// No service role can change the schema or the migration record.
	for _, svc := range Services {
		for _, stmt := range []string{
			`CREATE TABLE intruder (id int)`,
			`DROP TABLE devices`,
			`ALTER TABLE devices ADD COLUMN intruder int`,
			`TRUNCATE jobs`,
			`DELETE FROM schema_migrations`,
			`CREATE FUNCTION intruder() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		} {
			err := as(ctx, t, db, svc.Role, stmt)
			if err == nil || !(denied(err) || strings.Contains(err.Error(), "must be owner")) {
				t.Errorf("%s: %q should be refused, got %v", svc.Role, stmt, err)
			}
		}
	}
}

func TestRolesCanDoTheirWork(t *testing.T) {
	ctx, db := rolesDB(t)

	// Every granted privilege is usable: run the cheapest statement of
	// each kind against every table in the manifest.
	for _, svc := range Services {
		if err := as(ctx, t, db, svc.Role, `SELECT 1 FROM schema_migrations LIMIT 1`); err != nil {
			t.Errorf("%s cannot read schema_migrations: %v", svc.Role, err)
		}
		for table, privs := range Manifest[svc.Cmd] {
			for _, p := range privs {
				var stmt string
				switch p {
				case "SELECT":
					stmt = `SELECT 1 FROM ` + quoteIdent(table) + ` LIMIT 1`
				case "DELETE":
					stmt = `DELETE FROM ` + quoteIdent(table) + ` WHERE false`
				default:
					continue // INSERT and UPDATE need a row; covered below and by the live run
				}
				if err := as(ctx, t, db, svc.Role, stmt); err != nil {
					t.Errorf("%s: granted %s on %s but %q failed: %v", svc.Role, p, table, stmt, err)
				}
			}
		}
	}
}

// The gateway is not granted the event tables, yet a device it registers
// must still appear in the event stream: the triggers do that with the
// owner's rights (migration 0097).
func TestDeviceEventsAreWrittenForARoleWithoutEventTables(t *testing.T) {
	ctx, db := rolesDB(t)
	if _, ok := Manifest["acs"]["tmf_event_sequences"]; ok {
		t.Skip("the gateway is granted tmf_event_sequences; nothing to prove")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE acs_gateway`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO devices (id, oui_serial, online_status, last_inform_at)
		VALUES (gen_random_uuid(), 'ROLETEST-0001', 'ONLINE', now())`); err != nil {
		t.Fatalf("gateway role could not register a device: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tmf_events e JOIN devices d ON d.id = e.device_id
		WHERE d.oui_serial = 'ROLETEST-0001' AND e.event_type = 'DeviceSeen'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("DeviceSeen events = %d, want 1", n)
	}
}

// Apply sets privileges to exactly the manifest: one that is no longer in
// it is taken away, not left behind.
func TestApplyRemovesWhatTheManifestNoLongerGrants(t *testing.T) {
	ctx, db := rolesDB(t)
	if _, err := db.ExecContext(ctx, `GRANT SELECT ON operators TO acs_bss`); err != nil {
		t.Fatal(err)
	}
	if err := as(ctx, t, db, "acs_bss", `SELECT 1 FROM operators LIMIT 1`); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := as(ctx, t, db, "acs_bss", `SELECT 1 FROM operators LIMIT 1`); !denied(err) {
		t.Fatalf("stray grant survived Apply: %v", err)
	}
}

// roleDB returns a one-connection pool whose session is switched to a
// service role, so real repository code can be run with that role's
// privileges.
func roleDB(ctx context.Context, t *testing.T, role string) *sql.DB {
	t.Helper()
	db, err := store.Open(ctx, os.Getenv("ACS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `SET ROLE `+role); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRequireMigratedWorksForEveryServiceRole(t *testing.T) {
	ctx, _ := rolesDB(t)
	for _, svc := range Services {
		if err := store.RequireMigrated(ctx, roleDB(ctx, t, svc.Role)); err != nil {
			t.Errorf("%s: %v", svc.Role, err)
		}
	}
}

// The retention pruner names its tables from a rule list the extractor
// cannot read (Extra in roles.go). Run every rule as the API's role.
func TestRetentionRunsAsTheAPIRole(t *testing.T) {
	ctx, _ := rolesDB(t)
	everything := retention.Policy{
		SessionsDays: 1, AuditLogDays: 1, ParameterHistoryDays: 1, WebhookDeliveriesDays: 1,
		FinishedJobsDays: 1, StaleUploadSlotsDays: 1, ResetTokensDays: 1, CaptureSessionsDays: 1,
		WiFiInsightsDays: 1, TMFEventsDays: 1, SubscriberArchiveDays: 1,
	}
	if _, err := retention.Run(ctx, roleDB(ctx, t, "acs_api"), everything); err != nil {
		t.Fatalf("retention as acs_api: %v", err)
	}
}
