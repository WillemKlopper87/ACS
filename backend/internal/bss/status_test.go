package bss

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStatusPathsAskOnlyForWhatTheDeviceHas(t *testing.T) {
	if got := StatusPaths("DEVICE2", nil); !reflect.DeepEqual(got, []string{"Device.DeviceInfo.UpTime"}) {
		t.Errorf("no discovery: %v", got)
	}
	if got := StatusPaths("IGD1", nil); !reflect.DeepEqual(got, []string{"InternetGatewayDevice.DeviceInfo.UpTime"}) {
		t.Errorf("no discovery, TR-098: %v", got)
	}
	names := map[string]bool{
		"Device.DeviceInfo.UpTime":                  false,
		"Device.Hosts.HostNumberOfEntries":          false,
		"Device.Cellular.Interface.1.RSRP":          false,
		"Device.Cellular.Interface.2.RSRP":          false, // the first instance is taken
		"Device.Cellular.Interface.1.X_VENDOR_SINR": false, // not an exact leaf: not guessed at
		"Device.Cellular.Interface.1.RSSI":          false,
		"Device.WiFi.EasyMesh.STA.1.RSSI":           false, // not cellular
		"Device.Cellular.Interface.":                false, // an object, not a value
	}
	want := []string{
		"Device.DeviceInfo.UpTime", "Device.Hosts.HostNumberOfEntries",
		"Device.Cellular.Interface.1.RSRP", "Device.Cellular.Interface.1.RSSI",
	}
	if got := StatusPaths("DEVICE2", names); !reflect.DeepEqual(got, want) {
		t.Errorf("discovered: %v, want %v", got, want)
	}
	if got := StatusPaths("DEVICE2", map[string]bool{"Device.WiFi.SSID.1.SSID": true}); len(got) != 0 {
		t.Errorf("a device with none of them: %v", got)
	}
}

func TestBuildDeviceStatusKeepsUnreportedValuesNull(t *testing.T) {
	status := BuildDeviceStatus("acct-1", "dev-1", "gateway", map[string]CachedParameter{
		"Device.DeviceInfo.UpTime":         {Value: " 86400 ", UpdatedAt: "2026-11-17T08:05:00Z"},
		"Device.Hosts.HostNumberOfEntries": {Value: "7", UpdatedAt: "2026-11-17T08:00:00Z"},
		"Device.Cellular.Interface.1.RSRP": {Value: "-98", UpdatedAt: "2026-11-17T08:05:00Z"},
		"Device.Cellular.Interface.1.RSRQ": {Value: "n/a", UpdatedAt: "2026-11-01T00:00:00Z"}, // not a number: ignored, and not counted as read
	})
	body, _ := json.Marshal(status)
	want := `{"account_id":"acct-1","device_id":"dev-1","role":"gateway","uptime_seconds":86400,"lan_hosts":7,"signal":{"rsrp":-98,"rsrq":null,"sinr":null,"rssi":null},"read_at":"2026-11-17T08:00:00Z"}`
	if string(body) != want {
		t.Errorf("status = %s\nwant     %s", body, want)
	}
	empty, _ := json.Marshal(BuildDeviceStatus("acct-1", "dev-1", "gateway", map[string]CachedParameter{"Device.DeviceInfo.UpTime": {Value: "-1"}}))
	if string(empty) != `{"account_id":"acct-1","device_id":"dev-1","role":"gateway","uptime_seconds":null,"lan_hosts":null,"signal":null,"read_at":null}` {
		t.Errorf("nothing reported = %s", empty)
	}
}

// A router can report "NaN" or "Inf" for a signal level. Neither is a
// reading, and neither can be written as JSON.
func TestBuildDeviceStatusDropsNaNAndInf(t *testing.T) {
	status := BuildDeviceStatus("acct", "dev", "gateway", map[string]CachedParameter{
		"Device.Cellular.Interface.1.RSRP": {Value: "NaN"},
		"Device.Cellular.Interface.1.RSRQ": {Value: "-Inf"},
		"Device.Cellular.Interface.1.SINR": {Value: "Infinity"},
		"Device.Cellular.Interface.1.RSSI": {Value: "-67"},
	})
	if status.Signal == nil || status.Signal.RSSI == nil || *status.Signal.RSSI != -67 {
		t.Fatalf("signal = %+v; want the one real value", status.Signal)
	}
	if status.Signal.RSRP != nil || status.Signal.RSRQ != nil || status.Signal.SINR != nil {
		t.Errorf("NaN or Inf kept: %+v", status.Signal)
	}
	if _, err := json.Marshal(status); err != nil {
		t.Errorf("the status does not marshal: %v", err)
	}
	only := BuildDeviceStatus("acct", "dev", "gateway", map[string]CachedParameter{"Device.Cellular.Interface.1.RSRP": {Value: "NaN"}})
	if _, err := json.Marshal(only); err != nil || only.Signal != nil {
		t.Errorf("NaN alone: signal %+v, marshal err %v", only.Signal, err)
	}
}
