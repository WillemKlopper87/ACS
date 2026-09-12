package parameters

import (
	"context"
	"os"
	"testing"
	"time"

	"acs/internal/store"
)

// newParametersTestRepo mirrors the DSN-skip harness used throughout the
// backend's other DB-backed test suites (e.g.
// internal/devices/repository_test.go's newDevicesTestRepo): a clean,
// fully migrated schema per test, skipped entirely when no live Postgres
// is configured for tests.
//
// internal/parameters does not import internal/devices, so unlike
// newDevicesTestRepo this cannot seed a device via a devices.Repository
// method -- it seeds the minimal devices row directly with raw SQL, the
// same shape internal/store's own migration tests use.
func newParametersTestRepo(t *testing.T) (context.Context, *Repository, string) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	const deviceID = "11111111-1111-1111-1111-111111111111"
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, '001349-NR7101-A')`, deviceID); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	return ctx, NewRepository(db), deviceID
}

// TestInvalidateSubtreeRemovesOnlyMatchingKeys covers InvalidateSubtree's
// core contract: only keys under the given objPath prefix are dropped,
// sibling keys (including ones that share a prefix textually but are not
// actually under the subtree) survive untouched.
func TestInvalidateSubtreeRemovesOnlyMatchingKeys(t *testing.T) {
	ctx, r, deviceID := newParametersTestRepo(t)

	now := time.Now().UTC().Truncate(time.Second)
	err := r.Upsert(ctx, deviceID, map[string]CachedValue{
		"Device.WiFi.SSID.1.SSID":           {Value: "home", UpdatedAt: now, Source: SourceUSPNotify},
		"Device.WiFi.SSID.1.Enable":         {Value: "true", UpdatedAt: now, Source: SourceUSPNotify},
		"Device.WiFi.Radio.1.Channel":       {Value: "11", UpdatedAt: now, Source: SourceGetValues},
		"Device.WiFiExtra.Something":        {Value: "keep", UpdatedAt: now, Source: SourceGetValues},
		"Device.DeviceInfo.SoftwareVersion": {Value: "1.0", UpdatedAt: now, Source: SourceInform},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := r.InvalidateSubtree(ctx, deviceID, "Device.WiFi.SSID.1."); err != nil {
		t.Fatalf("InvalidateSubtree: %v", err)
	}

	got, err := r.Get(ctx, deviceID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	for _, removed := range []string{"Device.WiFi.SSID.1.SSID", "Device.WiFi.SSID.1.Enable"} {
		if _, ok := got[removed]; ok {
			t.Errorf("key %q survived InvalidateSubtree, want removed", removed)
		}
	}
	for _, kept := range []string{"Device.WiFi.Radio.1.Channel", "Device.WiFiExtra.Something", "Device.DeviceInfo.SoftwareVersion"} {
		if _, ok := got[kept]; !ok {
			t.Errorf("key %q was removed by InvalidateSubtree, want kept (not under the invalidated prefix)", kept)
		}
	}
	if len(got) != 3 {
		t.Errorf("cache has %d keys after InvalidateSubtree, want 3", len(got))
	}
}

// TestInvalidateSubtreeEmptyCacheNoop covers the contract that invalidating
// a subtree of a device with no cache row at all (never Upsert'd) is a
// no-op, not an error.
func TestInvalidateSubtreeEmptyCacheNoop(t *testing.T) {
	ctx, r, deviceID := newParametersTestRepo(t)

	if err := r.InvalidateSubtree(ctx, deviceID, "Device.WiFi."); err != nil {
		t.Fatalf("InvalidateSubtree on empty cache: %v", err)
	}

	got, err := r.Get(ctx, deviceID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cache has %d keys after invalidating an empty cache, want 0", len(got))
	}
}

// TestInvalidateSubtreeEmptyObjPathRejected is the regression test for
// final-review finding 1: strings.HasPrefix(x, "") is true for every x,
// so an empty objPath (reachable from a real ObjectCreation/
// ObjectDeletion Notify whose inner payload has no obj_path set -- see
// handler.go's own comment on that decode path) must never be allowed to
// silently wipe a device's entire parameter cache. InvalidateSubtree must
// reject it outright and leave every cached parameter untouched.
func TestInvalidateSubtreeEmptyObjPathRejected(t *testing.T) {
	ctx, r, deviceID := newParametersTestRepo(t)

	now := time.Now().UTC().Truncate(time.Second)
	seed := map[string]CachedValue{
		"Device.WiFi.SSID.1.SSID":           {Value: "home", UpdatedAt: now, Source: SourceUSPNotify},
		"Device.DeviceInfo.SoftwareVersion": {Value: "1.0", UpdatedAt: now, Source: SourceInform},
	}
	if err := r.Upsert(ctx, deviceID, seed); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := r.InvalidateSubtree(ctx, deviceID, ""); err == nil {
		t.Fatal("InvalidateSubtree(ctx, deviceID, \"\") = nil error, want an error rejecting the empty objPath")
	}

	got, err := r.Get(ctx, deviceID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for name := range seed {
		if _, ok := got[name]; !ok {
			t.Errorf("key %q was removed by InvalidateSubtree(\"\"), want every key left untouched", name)
		}
	}
	if len(got) != len(seed) {
		t.Errorf("cache has %d keys after InvalidateSubtree(\"\"), want %d (untouched)", len(got), len(seed))
	}
}

// TestInvalidateSubtreeNormalizesTrailingDot covers the trailing-dot
// normalization final-review finding 1 also asks for: a caller passing a
// non-dot-terminated path like "Device.WiFi.SSID.1" must not
// over-invalidate a sibling instance such as "Device.WiFi.SSID.10." via a
// bare textual prefix match ("Device.WiFi.SSID.10.Enable" does start with
// "Device.WiFi.SSID.1", but is not under "Device.WiFi.SSID.1.").
func TestInvalidateSubtreeNormalizesTrailingDot(t *testing.T) {
	ctx, r, deviceID := newParametersTestRepo(t)

	now := time.Now().UTC().Truncate(time.Second)
	err := r.Upsert(ctx, deviceID, map[string]CachedValue{
		"Device.WiFi.SSID.1.SSID":    {Value: "home", UpdatedAt: now, Source: SourceUSPNotify},
		"Device.WiFi.SSID.10.SSID":   {Value: "guest", UpdatedAt: now, Source: SourceUSPNotify},
		"Device.WiFi.SSID.10.Enable": {Value: "true", UpdatedAt: now, Source: SourceUSPNotify},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// No trailing dot given -- must be normalized to
	// "Device.WiFi.SSID.1." before matching, not used as a bare prefix.
	if err := r.InvalidateSubtree(ctx, deviceID, "Device.WiFi.SSID.1"); err != nil {
		t.Fatalf("InvalidateSubtree: %v", err)
	}

	got, err := r.Get(ctx, deviceID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got["Device.WiFi.SSID.1.SSID"]; ok {
		t.Error(`key "Device.WiFi.SSID.1.SSID" survived InvalidateSubtree, want removed`)
	}
	for _, kept := range []string{"Device.WiFi.SSID.10.SSID", "Device.WiFi.SSID.10.Enable"} {
		if _, ok := got[kept]; !ok {
			t.Errorf("key %q was removed by InvalidateSubtree(\"Device.WiFi.SSID.1\"), want kept -- it is a sibling instance (.10.), not under .1.'s subtree", kept)
		}
	}
	if len(got) != 2 {
		t.Errorf("cache has %d keys after InvalidateSubtree, want 2 (the sibling .10. instance's own two params)", len(got))
	}
}
