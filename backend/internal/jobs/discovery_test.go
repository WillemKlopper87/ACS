package jobs

import "testing"

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

// Nokia FastMile 1.2501.00.0798 drops the CWMP session when asked for
// GetParameterNames("Device.DynamicDNS.Client.", NextLevel=true); the walk
// must skip that object and continue instead of retrying it forever.
func TestSkipDiscoveryCursorAdvancesPastUnservableObject(t *testing.T) {
	p := ParameterDiscoveryPayload{
		Root: "Device.", Incremental: true,
		Cursor:  "Device.DynamicDNS.Client.",
		Pending: []string{"Device.DynamicDNS.Server.", "Device.Ethernet."},
		Names:   map[string]bool{"Device.DeviceInfo.SerialNumber": false},
	}
	done, err := SkipDiscoveryCursor(&p)
	if err != nil || done {
		t.Fatalf("SkipDiscoveryCursor = %v, %v; want not done, no error", done, err)
	}
	if p.RequestPath() != "Device.DynamicDNS.Server." || len(p.Pending) != 1 {
		t.Fatalf("walk did not advance to next pending object: %+v", p)
	}
	if !p.Visited["Device.DynamicDNS.Client."] || len(p.Skipped) != 1 || p.Skipped[0] != "Device.DynamicDNS.Client." {
		t.Fatalf("skipped object not recorded: visited=%v skipped=%v", p.Visited, p.Skipped)
	}

	// A later response re-listing the skipped object must not requeue it.
	done, err = AdvanceDiscovery(&p, map[string]bool{"Device.DynamicDNS.Server.Enable": true})
	if err != nil || done || p.RequestPath() != "Device.Ethernet." {
		t.Fatalf("walk after skip = %+v, %v", p, err)
	}
}

func TestSkipDiscoveryCursorOnLastObjectCompletesWalk(t *testing.T) {
	p := ParameterDiscoveryPayload{Root: "Device.", Incremental: true, Cursor: "Device.X_VENDOR."}
	done, err := SkipDiscoveryCursor(&p)
	if err != nil || !done || p.Cursor != "" {
		t.Fatalf("SkipDiscoveryCursor on last object = %v, %v, cursor %q; want done", done, err, p.Cursor)
	}
}

func TestSkipDiscoveryCursorIsBounded(t *testing.T) {
	p := ParameterDiscoveryPayload{Root: "Device.", Incremental: true, Cursor: "Device.A."}
	for i := 0; i < DiscoveryMaxSkipped; i++ {
		p.Skipped = append(p.Skipped, "Device.Skipped.")
	}
	if _, err := SkipDiscoveryCursor(&p); err == nil {
		t.Fatal("skip limit not enforced")
	}
	if _, err := SkipDiscoveryCursor(&ParameterDiscoveryPayload{Root: "Device."}); err == nil {
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
		"Device.Hosts.Host.":                                              true,
		"Device.DHCPv4.Server.Pool.1.Client.":                             true,
		"Device.WiFi.AccessPoint.2.AssociatedDevice.":                     true,
		"Device.WiFi.DataElements.AssociationEvent.AssociationEventData.": true,
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.BSS.2.STA.":    true,
		"Device.DeviceInfo.VendorLogFile.":                                true,
		"Device.DynamicDNS.Client.":                                       false,
		"Device.WiFi.SSID.":                                               false,
		"Device.IP.Interface.":                                            false,
	} {
		if got := isRuntimeTable(table); got != want {
			t.Errorf("isRuntimeTable(%q) = %v, want %v", table, got, want)
		}
	}
}
