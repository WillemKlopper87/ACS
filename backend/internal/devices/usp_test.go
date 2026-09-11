package devices

import (
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
