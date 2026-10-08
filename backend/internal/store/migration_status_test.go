package store

import "testing"

func TestMigrationCheckDoesNotApplyOrBackfill(t *testing.T) {
	ctx, db := preMigratedDB(t, "0001_devices.sql")
	if _, err := db.ExecContext(ctx, `DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	checks, err := CheckMigrations(ctx, db)
	if err != nil || len(checks) == 0 || checks[0].State != "pending" {
		t.Fatal(checks, err)
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("check created tracking table", exists, err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	checks, err = CheckMigrations(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range checks {
		if item.State != "applied" {
			t.Fatal(item)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET checksum=NULL WHERE filename='0001_devices.sql'`); err != nil {
		t.Fatal(err)
	}
	checks, err = CheckMigrations(ctx, db)
	if err != nil || checks[0].State != "unverified" {
		t.Fatal(checks, err)
	}
	var stillNull bool
	if err := db.QueryRowContext(ctx, `SELECT checksum IS NULL FROM schema_migrations WHERE filename='0001_devices.sql'`).Scan(&stillNull); err != nil || !stillNull {
		t.Fatal("check backfilled checksum", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET checksum='changed' WHERE filename='0001_devices.sql'; INSERT INTO schema_migrations(filename,checksum) VALUES('9999_future.sql','future')`); err != nil {
		t.Fatal(err)
	}
	checks, err = CheckMigrations(ctx, db)
	if err != nil || checks[0].State != "checksum_mismatch" || checks[len(checks)-1].State != "unknown_to_binary" {
		t.Fatal(checks, err)
	}
}
