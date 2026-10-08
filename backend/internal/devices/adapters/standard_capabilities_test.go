package adapters

import (
	"acs/internal/parameters"
	"testing"
)

func TestMeshProjectionGroupsDevicesAndRetainsBackhaulEvidence(t *testing.T) {
	path := "Device.WiFi.DataElements.Network.Device.2.MultiAPDevice.BackhaulMACAddress"
	result := ProjectWiFiTopology(map[string]bool{path: false}, map[string]parameters.CachedValue{path: {Value: "aa:bb:cc:dd:ee:ff"}, "Device.WiFi.SSID.1.SSID": {Value: "not-topology"}})
	if !result.Supported || len(result.Nodes) != 1 || result.Nodes[0].Path != "Device.WiFi.DataElements.Network.Device.2." || len(result.Nodes[0].Values) != 1 {
		t.Fatal(result)
	}
}
func TestModelNameAloneDoesNotQualifyFirmware(t *testing.T) {
	r := NewRegistry()
	if r.MatchFirmwareProfile("Zyxel", "NR7303-EU01V1F", "unknown-fw").Qualified {
		t.Fatal("model match incorrectly certified firmware")
	}
}
