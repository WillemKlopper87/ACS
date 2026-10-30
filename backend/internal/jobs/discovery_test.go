package jobs

import (
	"fmt"
	"testing"
)

func TestBoundedDiscoveryWalk(t *testing.T) {
	p := ParameterDiscoveryPayload{Root: "Device.", Incremental: true}
	done, err := AdvanceDiscovery(&p, map[string]bool{"Device.": false, "Device.WiFi.": false, "Device.DeviceInfo.": false})
	if err != nil || done || p.RequestPath() != "Device.DeviceInfo." {
		t.Fatalf("unexpected walk: %+v %v", p, err)
	}
	done, err = AdvanceDiscovery(&p, map[string]bool{"Device.DeviceInfo.SoftwareVersion": false})
	if err != nil || done || p.RequestPath() != "Device.WiFi." {
		t.Fatal(p, err)
	}
	done, err = AdvanceDiscovery(&p, map[string]bool{"Device.WiFi.Enable": true})
	if err != nil || !done || !p.Names["Device.WiFi.Enable"] {
		t.Fatal(p, err)
	}
}
func TestDiscoveryRejectsOutOfSubtreeAndOversizedTree(t *testing.T) {
	p := ParameterDiscoveryPayload{Root: "Device.WiFi."}
	if _, err := AdvanceDiscovery(&p, map[string]bool{"Device.DeviceInfo.SerialNumber": false}); err == nil {
		t.Error("out-of-subtree response accepted")
	}
	p = ParameterDiscoveryPayload{Root: "Device.", Names: map[string]bool{}}
	for i := 0; i < DiscoveryMaxNames; i++ {
		p.Names[string(rune(i))+"x"] = false
	}
	if _, err := AdvanceDiscovery(&p, map[string]bool{"Device.New": false}); err == nil {
		t.Error("name limit not enforced")
	}
}

// Nokia FastMile 1.2501.00.0798 drops the CWMP session mid-walk at objects
// that differ between runs: the first failure defers the object to the end of
// the walk, and only a second failure skips it.
func TestSkipDiscoveryCursorDefersOnceThenSkips(t *testing.T) {
	p := ParameterDiscoveryPayload{
		Root: "Device.", Incremental: true,
		Cursor:  "Device.Ethernet.Interface.",
		Pending: []string{"Device.DynamicDNS.Server.", "Device.IP."},
		Names:   map[string]bool{"Device.DeviceInfo.SerialNumber": false},
	}
	done, skipped, err := SkipDiscoveryCursor(&p)
	if err != nil || done || skipped {
		t.Fatalf("first failure = done %v, skipped %v, %v; want deferred", done, skipped, err)
	}
	if p.Cursor != "Device.DynamicDNS.Server." || len(p.Pending) != 2 || p.Pending[1] != "Device.Ethernet.Interface." {
		t.Fatalf("first failure did not defer the object to the end of the walk: %+v", p)
	}
	if len(p.Skipped) != 0 || p.Visited["Device.Ethernet.Interface."] {
		t.Fatalf("deferred object must not be skipped or visited yet: %+v", p)
	}

	// The retry succeeds when the device answers in a later session.
	p.Cursor, p.Pending = p.Pending[len(p.Pending)-1], p.Pending[:len(p.Pending)-1]
	if done, err := AdvanceDiscovery(&p, map[string]bool{"Device.Ethernet.Interface.1.": false}); err != nil || done {
		t.Fatalf("retry walk = %v, %v", done, err)
	}
	if _, ok := p.Names["Device.Ethernet.Interface.1."]; !ok {
		t.Fatal("retried table root should contribute its instances")
	}

	// A second failure on the same object skips it.
	p.Cursor = "Device.Ethernet.Interface."
	done, skipped, err = SkipDiscoveryCursor(&p)
	if err != nil || !skipped {
		t.Fatalf("second failure = done %v, skipped %v, %v; want skipped", done, skipped, err)
	}
	if len(p.Skipped) != 1 || p.Skipped[0] != "Device.Ethernet.Interface." || !p.Visited["Device.Ethernet.Interface."] {
		t.Fatalf("second failure not recorded as skipped: %+v", p)
	}
}

func TestSkipDiscoveryCursorOnLastObjectCompletesWalk(t *testing.T) {
	p := ParameterDiscoveryPayload{Root: "Device.", Incremental: true, Cursor: "Device.X_VENDOR.", Retried: map[string]bool{"Device.X_VENDOR.": true}}
	done, skipped, err := SkipDiscoveryCursor(&p)
	if err != nil || !done || !skipped || p.Cursor != "" {
		t.Fatalf("second failure on last object = done %v, skipped %v, %v, cursor %q; want done", done, skipped, err, p.Cursor)
	}
}

func TestSkipDiscoveryCursorIsBounded(t *testing.T) {
	p := ParameterDiscoveryPayload{Root: "Device.", Incremental: true, Cursor: "Device.A.", Retried: map[string]bool{"Device.A.": true}}
	for i := 0; i < DiscoveryMaxSkipped; i++ {
		p.Skipped = append(p.Skipped, "Device.Skipped.")
	}
	if _, _, err := SkipDiscoveryCursor(&p); err == nil {
		t.Fatal("skip limit not enforced")
	}
	if _, _, err := SkipDiscoveryCursor(&ParameterDiscoveryPayload{Root: "Device."}); err == nil {
		t.Fatal("skipping with no object in progress must fail")
	}
}

// Runtime-state tables (LAN clients, Wi-Fi events, log files) are walked at
// their lowest instance only; configuration tables keep every instance.
func TestDiscoveryCollapsesRuntimeTableInstances(t *testing.T) {
	p := ParameterDiscoveryPayload{Root: "Device.", Incremental: true, Cursor: "Device.Hosts.Host."}
	_, err := AdvanceDiscovery(&p, map[string]bool{
		"Device.Hosts.Host.":    false,
		"Device.Hosts.Host.3.":  false,
		"Device.Hosts.Host.1.":  false,
		"Device.Hosts.Host.22.": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := append([]string{p.Cursor}, p.Pending...); len(got) != 1 || got[0] != "Device.Hosts.Host.1." {
		t.Fatalf("runtime table walk = %v, want only Device.Hosts.Host.1.", got)
	}
	if _, ok := p.Names["Device.Hosts.Host.22."]; !ok {
		t.Fatal("collapsed instance should still be recorded as a discovered object, just not walked")
	}

	p = ParameterDiscoveryPayload{Root: "Device.", Incremental: true, Cursor: "Device.WiFi.SSID."}
	if _, err := AdvanceDiscovery(&p, map[string]bool{"Device.WiFi.SSID.1.": false, "Device.WiFi.SSID.2.": false}); err != nil {
		t.Fatal(err)
	}
	if got := append([]string{p.Cursor}, p.Pending...); len(got) != 2 {
		t.Fatalf("configuration table walk = %v, want both SSID instances", got)
	}
}

func TestIsRuntimeTable(t *testing.T) {
	for table, want := range map[string]bool{
		"Device.Hosts.Host.":                                                                        true,
		"Device.DHCPv4.Server.Pool.1.Client.":                                                       true,
		"Device.WiFi.AccessPoint.2.AssociatedDevice.":                                               true,
		"Device.WiFi.DataElements.AssociationEvent.AssociationEventData.":                           true,
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.BSS.2.STA.":                              true,
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.ScanResult.1.OpClassScan.1.ChannelScan.": true,
		"Device.FaultMgmt.SupportedAlarm.":                                                          true,
		"Device.InterfaceStack.":                                                                    true,
		"Device.DeviceInfo.VendorLogFile.":                                                          true,
		"Device.DynamicDNS.Client.":                                                                 false,
		"Device.WiFi.SSID.":                                                                         false,
		"Device.IP.Interface.":                                                                      false,
	} {
		if got := isRuntimeTable(table); got != want {
			t.Errorf("isRuntimeTable(%q) = %v, want %v", table, got, want)
		}
	}
}

func TestDiscoveryCapsInstancesOfUnknownTables(t *testing.T) {
	names := map[string]bool{}
	for i := 1; i <= DiscoveryMaxInstancesPerTable+10; i++ {
		names[fmt.Sprintf("Device.X_VENDOR.Table.%d.", i)] = false
	}
	p := ParameterDiscoveryPayload{Root: "Device.", Incremental: true, Cursor: "Device.X_VENDOR.Table."}
	if _, err := AdvanceDiscovery(&p, names); err != nil {
		t.Fatal(err)
	}
	walked := append([]string{p.Cursor}, p.Pending...)
	if len(walked) != DiscoveryMaxInstancesPerTable {
		t.Fatalf("walked %d instances, want the lowest %d", len(walked), DiscoveryMaxInstancesPerTable)
	}
	for _, path := range walked {
		var n int
		if _, err := fmt.Sscanf(path, "Device.X_VENDOR.Table.%d.", &n); err != nil || n > DiscoveryMaxInstancesPerTable {
			t.Fatalf("walked %q, want only instances 1..%d", path, DiscoveryMaxInstancesPerTable)
		}
	}
}
