package store

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"strings"
	"testing"
)

// preMigratedDB applies every migration up to (but not including) upTo,
// leaving the schema exactly as it was the moment upTo is about to run --
// the only way to test a data-backfill migration's effect on rows that
// predate it, since Migrate applies every embedded migration in one pass.
func preMigratedDB(t *testing.T, upTo string) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed migration test")
	}
	ctx := context.Background()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			checksum TEXT
		)`); err != nil {
		t.Fatal(err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if name >= upTo {
			break
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)`, name, checksum(sqlBytes)); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
	return ctx, db
}

// TestMigration0057NormalizesExistingOUI pins the backfill's actual job:
// a devices row stored under a non-canonical OUI form (as it could be
// for any row that predates cwmp.DeviceID.NormalizeOUI) is rewritten to
// the canonical form 0057 introduces, so it still matches its own future
// Informs instead of looking like a new device.
func TestMigration0057NormalizesExistingOUI(t *testing.T) {
	ctx, db := preMigratedDB(t, "0057_normalize_oui.sql")

	const deviceID = "11111111-1111-1111-1111-111111111111"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO devices (id, oui_serial, oui, product_class, serial_number) VALUES ($1, $2, $3, $4, $5)`,
		deviceID, "aa:bb:cc+NR5103+S230Q12345678", "aa:bb:cc", "NR5103", "S230Q12345678"); err != nil {
		t.Fatalf("seed pre-normalization device: %v", err)
	}
	const accountID = "acct-1"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial) VALUES (gen_random_uuid(), $1, $2, $3)`,
		accountID, deviceID, "aa:bb:cc+NR5103+S230Q12345678"); err != nil {
		t.Fatalf("seed pre-normalization mapping: %v", err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate (applying 0057 onward): %v", err)
	}

	var ouiSerial, oui string
	if err := db.QueryRowContext(ctx, `SELECT oui_serial, oui FROM devices WHERE id = $1`, deviceID).Scan(&ouiSerial, &oui); err != nil {
		t.Fatal(err)
	}
	if want := "AABBCC+NR5103+S230Q12345678"; ouiSerial != want {
		t.Errorf("devices.oui_serial = %q, want %q", ouiSerial, want)
	}
	if oui != "AABBCC" {
		t.Errorf("devices.oui = %q, want AABBCC", oui)
	}

	var mappingOUISerial string
	if err := db.QueryRowContext(ctx, `SELECT oui_serial FROM account_device_mappings WHERE device_id = $1`, deviceID).Scan(&mappingOUISerial); err != nil {
		t.Fatal(err)
	}
	if mappingOUISerial != ouiSerial {
		t.Errorf("account_device_mappings.oui_serial = %q, want it resynced to devices.oui_serial %q", mappingOUISerial, ouiSerial)
	}
}

// TestMigration0057LeavesCanonicalOUIUnchanged confirms the backfill is a
// true no-op for a row that was already canonical -- the overwhelming
// majority of real rows, since most CPEs already send uppercase,
// separator-free OUI.
func TestMigration0057LeavesCanonicalOUIUnchanged(t *testing.T) {
	ctx, db := preMigratedDB(t, "0057_normalize_oui.sql")

	const deviceID = "22222222-2222-2222-2222-222222222222"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO devices (id, oui_serial, oui, serial_number) VALUES ($1, $2, $3, $4)`,
		deviceID, "001349+S230Q12345678", "001349", "S230Q12345678"); err != nil {
		t.Fatalf("seed canonical device: %v", err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate (applying 0057 onward): %v", err)
	}

	var ouiSerial string
	if err := db.QueryRowContext(ctx, `SELECT oui_serial FROM devices WHERE id = $1`, deviceID).Scan(&ouiSerial); err != nil {
		t.Fatal(err)
	}
	if ouiSerial != "001349+S230Q12345678" {
		t.Errorf("an already-canonical oui_serial changed: got %q", ouiSerial)
	}
}
