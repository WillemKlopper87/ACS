package devices

import (
	"context"
	"errors"
	"testing"

	"acs/internal/cwmp"
)

// TestReconcileFromOnBoardUpdatesKnownDevice replaces
// TestUpsertFromOnBoardCreates: under the new contract, ReconcileFromOnBoard
// never creates a device -- it only updates one that already exists (here,
// via PreRegister, standing in for an operator's bulk import ahead of the
// device's first contact).
func TestReconcileFromOnBoardUpdatesKnownDevice(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}.NaturalKey()
	pre, err := r.PreRegister(ctx, ouiSerial, "Acme", "001122", "Router", "ABC123", nil, nil)
	if err != nil {
		t.Fatalf("PreRegister: %v", err)
	}

	d, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard: %v", err)
	}
	if d.ID != pre.ID {
		t.Fatalf("ReconcileFromOnBoard returned device %s, want the pre-registered %s", d.ID, pre.ID)
	}
	protocols := managementProtocols(t, ctx, r, d.ID)
	if len(protocols) != 1 || protocols[0] != "USP" {
		t.Errorf("management_protocols = %v, want [USP]", protocols)
	}
}

// TestReconcileFromOnBoardRejectsUnknownDevice covers this plan's central
// gate: an OUI+SerialNumber with no existing devices row is refused, not
// auto-created.
func TestReconcileFromOnBoardRejectsUnknownDevice(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	_, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("ReconcileFromOnBoard for an unknown device = %v, want ErrUnknownDevice", err)
	}

	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM devices`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("devices row count = %d after a rejected onboard, want 0 (nothing created)", count)
	}
}

// TestReconcileFromOnBoardMatchesExistingCWMPDevice is the checklist's
// central dual-stack assertion: a device onboarded first via CWMP
// (UpsertFromInform) and then reconciled via USP (ReconcileFromOnBoard)
// lands on the SAME devices row (same natural oui_serial key), and
// management_protocols ends up containing both 'CWMP' and 'USP' — neither
// overwrites the other.
func TestReconcileFromOnBoardMatchesExistingCWMPDevice(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	cwmpDevice, err := r.UpsertFromInform(ctx, cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}, nil)
	if err != nil {
		t.Fatalf("UpsertFromInform: %v", err)
	}

	uspDevice, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard: %v", err)
	}

	if uspDevice.ID != cwmpDevice.ID {
		t.Fatalf("ReconcileFromOnBoard matched a different row: %s != %s", uspDevice.ID, cwmpDevice.ID)
	}
	protocols := managementProtocols(t, ctx, r, cwmpDevice.ID)
	if !containsAll(protocols, "CWMP", "USP") {
		t.Errorf("management_protocols = %v, want to contain both CWMP and USP", protocols)
	}
}

// TestReconcileFromOnBoardIdempotent: calling ReconcileFromOnBoard twice
// for the same known device must not produce duplicate 'USP' entries in
// management_protocols.
func TestReconcileFromOnBoardIdempotent(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}.NaturalKey()
	if _, err := r.PreRegister(ctx, ouiSerial, "Acme", "001122", "Router", "ABC123", nil, nil); err != nil {
		t.Fatalf("PreRegister: %v", err)
	}

	d1, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("first ReconcileFromOnBoard: %v", err)
	}
	d2, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("second ReconcileFromOnBoard: %v", err)
	}
	if d1.ID != d2.ID {
		t.Fatalf("second ReconcileFromOnBoard matched a different row: %s != %s", d2.ID, d1.ID)
	}
	protocols := managementProtocols(t, ctx, r, d1.ID)
	if len(protocols) != 1 || protocols[0] != "USP" {
		t.Errorf("management_protocols after repeated ReconcileFromOnBoard = %v, want exactly [USP] (no duplicates)", protocols)
	}
}

// TestReconcileFromOnBoardSetsDataModelRootDevice2WhenUnknown covers the
// design's data_model_root fix: a device pre-registered via PreRegister
// (which never sets data_model_root, leaving it at the column's own
// 'UNKNOWN' default) and then onboarded via USP for the first time must
// end up DEVICE2, not stuck at UNKNOWN forever.
func TestReconcileFromOnBoardSetsDataModelRootDevice2WhenUnknown(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}.NaturalKey()
	pre, err := r.PreRegister(ctx, ouiSerial, "Acme", "001122", "Router", "ABC123", nil, nil)
	if err != nil {
		t.Fatalf("PreRegister: %v", err)
	}
	if pre.DataModelRoot != "UNKNOWN" {
		t.Fatalf("pre-registered device DataModelRoot = %q, want UNKNOWN (test assumption wrong)", pre.DataModelRoot)
	}

	d, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard: %v", err)
	}
	if d.DataModelRoot != DataModelRootDevice2 {
		t.Errorf("DataModelRoot = %q, want %q", d.DataModelRoot, DataModelRootDevice2)
	}
}

// TestReconcileFromOnBoardDoesNotOverwriteExistingDataModelRoot covers the
// other half: a CWMP-discovered data_model_root (which might legitimately
// be IGD1) must survive a later USP onboarding of the same physical
// device.
func TestReconcileFromOnBoardDoesNotOverwriteExistingDataModelRoot(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	cwmpDevice, err := r.UpsertFromInform(ctx, cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}, nil)
	if err != nil {
		t.Fatalf("UpsertFromInform: %v", err)
	}
	if err := r.UpdateDataModelRoot(ctx, cwmpDevice.ID, DataModelRootIGD1); err != nil {
		t.Fatalf("UpdateDataModelRoot: %v", err)
	}

	uspDevice, err := r.ReconcileFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard: %v", err)
	}
	if uspDevice.DataModelRoot != DataModelRootIGD1 {
		t.Errorf("DataModelRoot after USP onboarding of an existing CWMP(IGD1) device = %q, want unchanged %q", uspDevice.DataModelRoot, DataModelRootIGD1)
	}
}

// TestReconcileFromOnBoardRejectsEmptyOUI and
// TestReconcileFromOnBoardRejectsEmptySerialNumber cover final-review
// finding 1 (from the USP job dispatch plan): an empty OUI or
// SerialNumber must be rejected before any query runs, mirroring
// cmd/acs/session.go's CWMP Inform guard. These deliberately construct a
// bare *Repository{} rather than going through newDevicesTestRepo/a live
// Postgres connection: the guard must fire before any query is issued, so
// a nil *sql.DB proves that (a query attempt against a nil db would
// panic, failing the test) without needing ACS_TEST_POSTGRES_DSN.
func TestReconcileFromOnBoardRejectsEmptyOUI(t *testing.T) {
	r := &Repository{}
	_, err := r.ReconcileFromOnBoard(context.Background(), "", "Router", "ABC123")
	if !errors.Is(err, ErrEmptyIdentity) {
		t.Fatalf("ReconcileFromOnBoard with empty OUI = %v, want ErrEmptyIdentity", err)
	}
}

func TestReconcileFromOnBoardRejectsEmptySerialNumber(t *testing.T) {
	r := &Repository{}
	_, err := r.ReconcileFromOnBoard(context.Background(), "001122", "Router", "")
	if !errors.Is(err, ErrEmptyIdentity) {
		t.Fatalf("ReconcileFromOnBoard with empty SerialNumber = %v, want ErrEmptyIdentity", err)
	}
}

// TestReconcileFromOnBoardAllowsEmptyProductClass proves the guard
// matches the CWMP Inform path's exact shape (session.go:218):
// ProductClass alone being empty is not rejected, only an empty OUI or
// SerialNumber is -- and that a device pre-registered with an empty
// ProductClass is still matched correctly.
func TestReconcileFromOnBoardAllowsEmptyProductClass(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	ouiSerial := cwmp.DeviceID{OUI: "001122", ProductClass: "", SerialNumber: "ABC123"}.NaturalKey()
	if _, err := r.PreRegister(ctx, ouiSerial, "Acme", "001122", "", "ABC123", nil, nil); err != nil {
		t.Fatalf("PreRegister: %v", err)
	}

	d, err := r.ReconcileFromOnBoard(ctx, "001122", "", "ABC123")
	if err != nil {
		t.Fatalf("ReconcileFromOnBoard with empty ProductClass: %v", err)
	}
	if d.OUI != "001122" || d.SerialNumber != "ABC123" {
		t.Errorf("device = %+v, want OUI=001122 SerialNumber=ABC123", d)
	}
}
