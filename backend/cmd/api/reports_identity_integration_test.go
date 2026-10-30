package main

import (
	"testing"

	"acs/internal/devices"
	"acs/internal/parameters"
)

// TestIntegration_ReportIdentity checks the export's SQL subset end to
// end: the SSID column is the FastMile's primary network (not SSID.1 by
// accident of position, not a guest SSID) and the MAC is the Ethernet
// interface, not a BSSID or a Wi-Fi client; no passphrase is loaded.
func TestIntegration_ReportIdentity(t *testing.T) {
	e := newTestEnv(t)
	gw, beacon := e.device("R001", nil), e.device("R002", nil)

	names, cache := fastMileWiFiTree()
	cache["Device.WiFi.SSID.1.SSID"] = parameters.CachedValue{Value: "SENT-5G"}
	names["Device.Ethernet.Interface.1.MACAddress"] = false
	cache["Device.Ethernet.Interface.1.MACAddress"] = parameters.CachedValue{Value: "10:00:00:00:00:01"}
	names["Device.WiFi.AccessPoint.1.AssociatedDevice.1.MACAddress"] = false
	cache["Device.WiFi.AccessPoint.1.AssociatedDevice.1.MACAddress"] = parameters.CachedValue{Value: "aa:aa:aa:aa:aa:aa"}
	cache["Device.WiFi.AccessPoint.1.Security.KeyPassphrase"] = parameters.CachedValue{Value: "must-not-load"}
	if err := e.h.devices.UpdateDataModelRoot(e.ctx, gw, devices.DataModelRootDevice2); err != nil {
		t.Fatal(err)
	}
	if err := e.h.params.SaveNames(e.ctx, gw, names); err != nil {
		t.Fatal(err)
	}
	if err := e.h.params.Upsert(e.ctx, gw, cache); err != nil {
		t.Fatal(err)
	}
	// Beacon: never discovered here, no Device.WiFi., Ethernet.Link MACs only.
	if err := e.h.params.Upsert(e.ctx, beacon, map[string]parameters.CachedValue{
		"Device.Ethernet.Link.1.MACAddress": {Value: "20:00:00:00:00:01"},
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := e.h.devices.ReportRows(e.ctx, nil, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, leaked := row.IdentityValues["Device.WiFi.AccessPoint.1.Security.KeyPassphrase"]; leaked {
			t.Errorf("report loaded a passphrase for %s", row.SerialNumber)
		}
	}
	resolveReportIdentity(rows)
	got := map[string][2]string{}
	for _, row := range rows {
		got[row.SerialNumber] = [2]string{row.SSID, row.MACAddress}
	}
	if got["R001"] != [2]string{"SENT-5G", "10:00:00:00:00:01"} {
		t.Errorf("gateway row = %v, want SENT-5G / Ethernet.Interface.1 MAC", got["R001"])
	}
	if got["R002"] != [2]string{"", "20:00:00:00:00:01"} {
		t.Errorf("beacon row = %v, want no SSID / Ethernet.Link.1 MAC", got["R002"])
	}
}
