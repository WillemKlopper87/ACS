package devices

import (
	"reflect"
	"testing"

	"acs/internal/cwmp"
)

func TestGatewayInfoReadPaths(t *testing.T) {
	beacon := map[string]bool{
		"Device.GatewayInfo.":                false,
		GatewayInfoManufacturerOUI:           false,
		GatewayInfoProductClass:              false,
		GatewayInfoSerialNumber:              false,
		"Device.X_ALU-COM_Wifi.WorkRole":     false,
		"Device.DeviceInfo.SoftwareVersion":  false,
		"Device.ManagementServer.URL":        true,
		"Device.GatewayInfo.X_Vendor_Unused": false,
	}
	if got, want := GatewayInfoReadPaths(beacon), GatewayInfoPaths; !reflect.DeepEqual(got, want) {
		t.Fatalf("GatewayInfoReadPaths(beacon) = %v, want %v", got, want)
	}

	noProductClass := map[string]bool{GatewayInfoManufacturerOUI: false, GatewayInfoSerialNumber: false}
	if got, want := GatewayInfoReadPaths(noProductClass), []string{GatewayInfoManufacturerOUI, GatewayInfoSerialNumber}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GatewayInfoReadPaths(no product class) = %v, want %v", got, want)
	}

	gateway := map[string]bool{"Device.WiFi.SSID.1.SSID": true, "Device.DeviceInfo.SerialNumber": false}
	if got := GatewayInfoReadPaths(gateway); got != nil {
		t.Fatalf("GatewayInfoReadPaths(gateway without GatewayInfo) = %v, want nil", got)
	}
}

func TestGatewayIdentityFromValues(t *testing.T) {
	// The FastMile 5G Gateway 2 the Beacon 3.1 sat behind in the field test.
	fastMile := cwmp.DeviceID{OUI: "A4FC77", ProductClass: "3TG02241", SerialNumber: "CSC25430018F"}

	cases := []struct {
		name   string
		values map[string]string
		want   cwmp.DeviceID
		ok     bool
	}{
		{
			name: "complete",
			values: map[string]string{
				GatewayInfoManufacturerOUI: "A4FC77", GatewayInfoProductClass: "3TG02241", GatewayInfoSerialNumber: "CSC25430018F",
			},
			want: fastMile, ok: true,
		},
		{
			name: "oui case and separators normalised, whitespace trimmed",
			values: map[string]string{
				GatewayInfoManufacturerOUI: " a4:fc:77 ", GatewayInfoProductClass: "3TG02241 ", GatewayInfoSerialNumber: " CSC25430018F",
			},
			want: fastMile, ok: true,
		},
		{
			name:   "no product class",
			values: map[string]string{GatewayInfoManufacturerOUI: "A4FC77", GatewayInfoSerialNumber: "CSC25430018F"},
			want:   cwmp.DeviceID{OUI: "A4FC77", SerialNumber: "CSC25430018F"}, ok: true,
		},
		{name: "blank values (standalone, no upstream gateway)", values: map[string]string{
			GatewayInfoManufacturerOUI: "", GatewayInfoProductClass: "", GatewayInfoSerialNumber: "",
		}},
		{name: "serial missing", values: map[string]string{GatewayInfoManufacturerOUI: "A4FC77"}},
		{name: "oui missing", values: map[string]string{GatewayInfoSerialNumber: "CSC25430018F"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := GatewayIdentityFromValues(tc.values)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("GatewayIdentityFromValues() = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}

	got, _ := GatewayIdentityFromValues(map[string]string{
		GatewayInfoManufacturerOUI: "a4fc77", GatewayInfoProductClass: "3TG02241", GatewayInfoSerialNumber: "CSC25430018F",
	})
	if got.NaturalKey() != fastMile.NaturalKey() {
		t.Fatalf("natural key %q, want the gateway's own %q", got.NaturalKey(), fastMile.NaturalKey())
	}
}

// TestLinkGatewayLifecycle covers resolution, the self-link guard, change
// detection, adoption of a satellite that reported its gateway first, and
// ON DELETE SET NULL.
func TestLinkGatewayLifecycle(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	gatewayID := cwmp.DeviceID{Manufacturer: "Nokia", OUI: "A4FC77", ProductClass: "3TG02241", SerialNumber: "CSC25430018F"}
	beaconID := cwmp.DeviceID{Manufacturer: "ALCL", OUI: "F89B6E", ProductClass: "Beacon 3.1", SerialNumber: "ALCLB0000001"}

	beacon, err := r.UpsertFromInform(ctx, beaconID, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatal(err)
	}

	// Satellite reports a gateway that has not informed yet.
	link, err := r.LinkGateway(ctx, beacon.ID, gatewayID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !link.Changed || link.GatewayDeviceID != nil || link.GatewayOUISerial != gatewayID.NaturalKey() {
		t.Fatalf("pre-gateway link = %+v", link)
	}

	gateway, err := r.UpsertFromInform(ctx, gatewayID, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := r.AdoptSatellites(ctx, gateway); err != nil || n != 1 {
		t.Fatalf("AdoptSatellites = %d, %v; want 1", n, err)
	}
	got, err := r.Get(ctx, beacon.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GatewayDeviceID == nil || *got.GatewayDeviceID != gateway.ID {
		t.Fatalf("adopted gateway_device_id = %v, want %s", got.GatewayDeviceID, gateway.ID)
	}

	// Same values again: no change.
	if link, err = r.LinkGateway(ctx, beacon.ID, gatewayID, true); err != nil || link.Changed {
		t.Fatalf("repeat link = %+v, %v; want unchanged", link, err)
	}

	sats, err := r.ListSatellites(ctx, gateway.ID, nil, false)
	if err != nil || len(sats) != 1 || sats[0].ID != beacon.ID {
		t.Fatalf("ListSatellites = %v, %v", sats, err)
	}
	if sats, err = r.ListSatellites(ctx, gateway.ID, []string{}, true); err != nil || len(sats) != 0 {
		t.Fatalf("scoped ListSatellites with no customers = %v, %v; want none", sats, err)
	}

	rel, err := r.MeshRelationsFor(ctx, []string{beacon.ID, gateway.ID}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Gateway[beacon.ID] != gateway.ID || !reflect.DeepEqual(rel.Satellites[gateway.ID], []string{beacon.ID}) {
		t.Fatalf("MeshRelationsFor = %+v", rel)
	}

	// ProductClass mismatch still resolves by unique OUI + serial.
	loose := gatewayID
	loose.ProductClass = "FastMile 5G Gateway 2"
	if link, err = r.LinkGateway(ctx, beacon.ID, loose, true); err != nil || link.GatewayDeviceID == nil || *link.GatewayDeviceID != gateway.ID {
		t.Fatalf("loose link = %+v, %v", link, err)
	}

	// A device never links to itself.
	if link, err = r.LinkGateway(ctx, gateway.ID, gatewayID, true); err != nil || link.GatewayDeviceID != nil {
		t.Fatalf("self link = %+v, %v; want unresolved", link, err)
	}

	// Blank GatewayInfo clears the link.
	if link, err = r.LinkGateway(ctx, beacon.ID, cwmp.DeviceID{}, false); err != nil || !link.Changed {
		t.Fatalf("clear link = %+v, %v", link, err)
	}
	if got, _ = r.Get(ctx, beacon.ID); got.GatewayDeviceID != nil || got.GatewayOUISerial != nil {
		t.Fatalf("cleared link still set: %+v / %+v", got.GatewayDeviceID, got.GatewayOUISerial)
	}

	// ON DELETE SET NULL keeps the reported key.
	if _, err = r.LinkGateway(ctx, beacon.ID, gatewayID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM devices WHERE id = $1`, gateway.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = r.Get(ctx, beacon.ID); got.GatewayDeviceID != nil || got.GatewayOUISerial == nil {
		t.Fatalf("after gateway delete: id=%v key=%v; want id cleared, key kept", got.GatewayDeviceID, got.GatewayOUISerial)
	}
}
