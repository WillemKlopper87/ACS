package subscriptions

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"acs/internal/store"
)

// newSubscriptionsTestRepo mirrors the DSN-skip harness used throughout the
// backend's other DB-backed test suites (e.g. internal/devices/repository_test.go's
// newDevicesTestRepo): a clean, fully migrated schema per test, skipped
// entirely when no live Postgres is configured for tests. It also seeds a
// single devices row directly via SQL (rather than importing
// internal/devices, which this package must not depend on) since
// usp_subscriptions.device_id is a foreign key into devices.
func newSubscriptionsTestRepo(t *testing.T) (context.Context, *Repository, string) {
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

	deviceID := uuid.New().String()
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, $2)`, deviceID, "seed-"+deviceID); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	return ctx, NewRepository(db), deviceID
}

func TestRepositoryCreateByDeviceDelete(t *testing.T) {
	ctx, r, deviceID := newSubscriptionsTestRepo(t)

	sub1 := Subscription{
		ID:            uuid.New().String(),
		DeviceID:      deviceID,
		NotifType:     "ValueChange",
		ReferenceList: []string{"Device.WiFi.SSID.1.SSID"},
		Persistent:    true,
		CreatedBy:     "test",
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	sub2 := Subscription{
		ID:            uuid.New().String(),
		DeviceID:      deviceID,
		NotifType:     "ObjectCreation",
		ReferenceList: []string{"Device.WiFi.AccessPoint."},
		Persistent:    false,
		CreatedBy:     "test",
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}

	if err := r.Create(ctx, sub1); err != nil {
		t.Fatalf("Create sub1: %v", err)
	}
	if err := r.Create(ctx, sub2); err != nil {
		t.Fatalf("Create sub2: %v", err)
	}

	got, err := r.ByDevice(ctx, deviceID)
	if err != nil {
		t.Fatalf("ByDevice: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ByDevice after two Creates = %d subscriptions, want 2", len(got))
	}
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	want := []Subscription{sub1, sub2}
	sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
	for i := range want {
		assertSubscriptionEqual(t, got[i], want[i])
	}

	if err := r.Delete(ctx, sub1.ID); err != nil {
		t.Fatalf("Delete sub1: %v", err)
	}

	got, err = r.ByDevice(ctx, deviceID)
	if err != nil {
		t.Fatalf("ByDevice after Delete: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ByDevice after Delete = %d subscriptions, want 1", len(got))
	}
	assertSubscriptionEqual(t, got[0], sub2)
}

func assertSubscriptionEqual(t *testing.T, got, want Subscription) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.DeviceID != want.DeviceID {
		t.Errorf("DeviceID = %q, want %q", got.DeviceID, want.DeviceID)
	}
	if got.NotifType != want.NotifType {
		t.Errorf("NotifType = %q, want %q", got.NotifType, want.NotifType)
	}
	if len(got.ReferenceList) != len(want.ReferenceList) {
		t.Errorf("ReferenceList = %v, want %v", got.ReferenceList, want.ReferenceList)
	} else {
		for i := range want.ReferenceList {
			if got.ReferenceList[i] != want.ReferenceList[i] {
				t.Errorf("ReferenceList = %v, want %v", got.ReferenceList, want.ReferenceList)
				break
			}
		}
	}
	if got.Persistent != want.Persistent {
		t.Errorf("Persistent = %v, want %v", got.Persistent, want.Persistent)
	}
	if got.CreatedBy != want.CreatedBy {
		t.Errorf("CreatedBy = %q, want %q", got.CreatedBy, want.CreatedBy)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
}
