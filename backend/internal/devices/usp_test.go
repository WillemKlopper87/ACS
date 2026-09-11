package devices

import (
	"context"
	"errors"
	"testing"

	"acs/internal/cwmp"
)

func TestUpsertFromOnBoardCreates(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	d, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("UpsertFromOnBoard: %v", err)
	}
	if d.ID == "" {
		t.Fatal("expected a non-empty device ID")
	}
	wantKey := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}.NaturalKey()
	if d.OUISerial != wantKey {
		t.Errorf("OUISerial = %q, want %q", d.OUISerial, wantKey)
	}
	protocols := managementProtocols(t, ctx, r, d.ID)
	if len(protocols) != 1 || protocols[0] != "USP" {
		t.Errorf("management_protocols = %v, want [USP]", protocols)
	}
}

// TestUpsertFromOnBoardMatchesExistingCWMPDevice is the checklist's central
// dual-stack assertion: a device onboarded first via CWMP (UpsertFromInform)
// and then reconciled via USP (UpsertFromOnBoard) lands on the SAME devices
// row (same natural oui_serial key), and management_protocols ends up
// containing both 'CWMP' and 'USP' — neither overwrites the other.
func TestUpsertFromOnBoardMatchesExistingCWMPDevice(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	cwmpDevice, err := r.UpsertFromInform(ctx, cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}, nil)
	if err != nil {
		t.Fatalf("UpsertFromInform: %v", err)
	}

	uspDevice, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("UpsertFromOnBoard: %v", err)
	}

	if uspDevice.ID != cwmpDevice.ID {
		t.Fatalf("UpsertFromOnBoard created a new row: %s != %s", uspDevice.ID, cwmpDevice.ID)
	}
	protocols := managementProtocols(t, ctx, r, cwmpDevice.ID)
	if !containsAll(protocols, "CWMP", "USP") {
		t.Errorf("management_protocols = %v, want to contain both CWMP and USP", protocols)
	}
}

// TestUpsertFromOnBoardIdempotent: calling UpsertFromOnBoard twice for the
// same device must not produce duplicate 'USP' entries in
// management_protocols.
func TestUpsertFromOnBoardIdempotent(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	d1, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("first UpsertFromOnBoard: %v", err)
	}
	d2, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("second UpsertFromOnBoard: %v", err)
	}
	if d1.ID != d2.ID {
		t.Fatalf("second UpsertFromOnBoard created a new row: %s != %s", d2.ID, d1.ID)
	}
	protocols := managementProtocols(t, ctx, r, d1.ID)
	if len(protocols) != 1 || protocols[0] != "USP" {
		t.Errorf("management_protocols after repeated UpsertFromOnBoard = %v, want exactly [USP] (no duplicates)", protocols)
	}
}

// TestUpsertFromOnBoardSetsDataModelRootDevice2 covers final-review finding
// 4: USP agents' data_model_root is always DEVICE2 (design spec §5.3), so a
// fresh USP-only device must not be left on the devices table's 'UNKNOWN'
// default.
func TestUpsertFromOnBoardSetsDataModelRootDevice2(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	d, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("UpsertFromOnBoard: %v", err)
	}
	if d.DataModelRoot != DataModelRootDevice2 {
		t.Errorf("DataModelRoot = %q, want %q", d.DataModelRoot, DataModelRootDevice2)
	}
}

// TestUpsertFromOnBoardDoesNotOverwriteExistingDataModelRoot covers the
// other half of finding 4: a CWMP-discovered data_model_root (which might
// legitimately be IGD1) must survive a later USP onboarding of the same
// physical device — the ON CONFLICT path must not touch the column at all.
func TestUpsertFromOnBoardDoesNotOverwriteExistingDataModelRoot(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	cwmpDevice, err := r.UpsertFromInform(ctx, cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}, nil)
	if err != nil {
		t.Fatalf("UpsertFromInform: %v", err)
	}
	if err := r.UpdateDataModelRoot(ctx, cwmpDevice.ID, DataModelRootIGD1); err != nil {
		t.Fatalf("UpdateDataModelRoot: %v", err)
	}

	uspDevice, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("UpsertFromOnBoard: %v", err)
	}
	if uspDevice.DataModelRoot != DataModelRootIGD1 {
		t.Errorf("DataModelRoot after USP onboarding of an existing CWMP(IGD1) device = %q, want unchanged %q", uspDevice.DataModelRoot, DataModelRootIGD1)
	}
}

// TestUpsertFromOnBoardRejectsEmptyOUI and
// TestUpsertFromOnBoardRejectsEmptySerialNumber cover final-review finding
// 1: an empty OUI or SerialNumber must be rejected before any devices row
// is created or matched, mirroring cmd/acs/session.go's CWMP Inform guard.
// These deliberately construct a bare *Repository{} rather than going
// through newDevicesTestRepo/a live Postgres connection: the guard must
// fire before any query is issued, so a nil *sql.DB proves that (a query
// attempt against a nil db would panic, failing the test) without needing
// ACS_TEST_POSTGRES_DSN.
func TestUpsertFromOnBoardRejectsEmptyOUI(t *testing.T) {
	r := &Repository{}
	_, err := r.UpsertFromOnBoard(context.Background(), "", "Router", "ABC123")
	if !errors.Is(err, ErrEmptyIdentity) {
		t.Fatalf("UpsertFromOnBoard with empty OUI = %v, want ErrEmptyIdentity", err)
	}
}

func TestUpsertFromOnBoardRejectsEmptySerialNumber(t *testing.T) {
	r := &Repository{}
	_, err := r.UpsertFromOnBoard(context.Background(), "001122", "Router", "")
	if !errors.Is(err, ErrEmptyIdentity) {
		t.Fatalf("UpsertFromOnBoard with empty SerialNumber = %v, want ErrEmptyIdentity", err)
	}
}

// TestUpsertFromOnBoardAllowsEmptyProductClass proves the guard matches
// the CWMP Inform path's exact shape (session.go:218): ProductClass alone
// being empty is not rejected, only an empty OUI or SerialNumber is.
func TestUpsertFromOnBoardAllowsEmptyProductClass(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	d, err := r.UpsertFromOnBoard(ctx, "001122", "", "ABC123")
	if err != nil {
		t.Fatalf("UpsertFromOnBoard with empty ProductClass: %v", err)
	}
	if d.OUI != "001122" || d.SerialNumber != "ABC123" {
		t.Errorf("device = %+v, want OUI=001122 SerialNumber=ABC123", d)
	}
}
