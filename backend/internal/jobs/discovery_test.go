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
