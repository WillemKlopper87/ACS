package adapters

import (
	"strconv"
	"testing"

	"acs/internal/devices"
	"acs/internal/parameters"
)

// fastMileReportTree: Nokia FastMile 5G Gateway 2 field layout — SENT-5G
// on SSID.1 (2.4 GHz) + SSID.5 (5 GHz), guest SSID.2/6, disabled factory
// SSID.3/4/7/8 — plus the MAC-bearing paths its discovery lists: per-SSID
// BSSIDs, Ethernet.Interface.1, Ethernet.Link.2, and a Wi-Fi client.
func fastMileReportTree() (map[string]bool, map[string]parameters.CachedValue) {
	names := map[string]bool{}
	cache := map[string]parameters.CachedValue{}
	set := func(path, value string) {
		names[path] = false
		cache[path] = parameters.CachedValue{Value: value}
	}
	set("Device.WiFi.Radio.1.OperatingFrequencyBand", "2.4GHz")
	set("Device.WiFi.Radio.2.OperatingFrequencyBand", "5GHz")
	for i := 1; i <= 8; i++ {
		inst := strconv.Itoa(i)
		radio, name, guest, enable := "Device.WiFi.Radio.1", "SENT-5G", "0", "1"
		if i > 4 {
			radio = "Device.WiFi.Radio.2"
		}
		switch i {
		case 2, 6:
			name, guest = "SENT-Guest", "1"
		case 3, 4, 7, 8:
			name, enable = "NOKIA-19C9-"+inst, "0"
		}
		p := "Device.WiFi.SSID." + inst + "."
		set(p+"SSID", name)
		set(p+"Enable", enable)
		set(p+"LowerLayers", radio)
		set(p+"X_ALU-COM_IsGuestSSID", guest)
		set(p+"MACAddress", "02:00:00:00:00:0"+inst) // BSSID
		set("Device.WiFi.AccessPoint."+inst+".SSIDReference", "Device.WiFi.SSID."+inst+".")
	}
	set("Device.WiFi.AccessPoint.1.AssociatedDevice.1.MACAddress", "aa:aa:aa:aa:aa:aa") // a client
	set("Device.Ethernet.Link.2.MACAddress", "10:00:00:00:00:02")
	set("Device.Ethernet.Interface.1.MACAddress", "10:00:00:00:00:01")
	return names, cache
}

func TestReportSSIDIsThePrimaryNetwork(t *testing.T) {
	names, cache := fastMileReportTree()
	if got := ReportSSID(devices.DataModelRootDevice2, names, cache); got != "SENT-5G" {
		t.Errorf("FastMile SSID = %q, want SENT-5G (not the guest or factory SSIDs)", got)
	}

	cache["Device.WiFi.SSID.5.SSID"] = parameters.CachedValue{Value: "SENT-5G-5GHz"}
	if got := ReportSSID(devices.DataModelRootDevice2, names, cache); got != "SENT-5G / SENT-5G-5GHz" {
		t.Errorf("split SSID = %q, want both bands", got)
	}

	// Guest flag discovered but not cached: no guess that could name a guest SSID.
	delete(cache, "Device.WiFi.SSID.2.X_ALU-COM_IsGuestSSID")
	if got := ReportSSID(devices.DataModelRootDevice2, names, cache); got != "" {
		t.Errorf("incomplete view SSID = %q, want empty", got)
	}

	// Never discovered: the cached keys stand in for the tree.
	_, cache = fastMileReportTree()
	if got := ReportSSID(devices.DataModelRootDevice2, nil, cache); got != "SENT-5G" {
		t.Errorf("undiscovered SSID = %q, want SENT-5G from the cache", got)
	}
}

func TestReportSSIDWithoutWiFiAndTR098(t *testing.T) {
	beacon := map[string]parameters.CachedValue{"Device.Ethernet.Link.1.MACAddress": {Value: "20:00:00:00:00:01"}}
	if got := ReportSSID(devices.DataModelRootDevice2, map[string]bool{"Device.Ethernet.Link.1.MACAddress": false}, beacon); got != "" {
		t.Errorf("Beacon SSID = %q, want empty (Wi-Fi run by the gateway)", got)
	}
	igd := map[string]parameters.CachedValue{"InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.SSID": {Value: "Legacy"}}
	if got := ReportSSID(devices.DataModelRootIGD1, nil, igd); got != "Legacy" {
		t.Errorf("TR-098 SSID = %q, want Legacy", got)
	}
}

func TestDeviceMACAddressPrefersEthernetIdentity(t *testing.T) {
	names, cache := fastMileReportTree()
	if got := DeviceMACAddress(devices.DataModelRootDevice2, names, cache); got != "10:00:00:00:00:01" {
		t.Errorf("FastMile MAC = %q, want Ethernet.Interface.1 (never a BSSID or client)", got)
	}

	// An all-zero Interface.1 is skipped: fall to Ethernet.Link.
	cache["Device.Ethernet.Interface.1.MACAddress"] = parameters.CachedValue{Value: "00:00:00:00:00:00"}
	if got := DeviceMACAddress(devices.DataModelRootDevice2, names, cache); got != "10:00:00:00:00:02" {
		t.Errorf("MAC with zero Interface.1 = %q, want Ethernet.Link.2", got)
	}

	// A value the discovery does not list is not trusted.
	delete(names, "Device.Ethernet.Link.2.MACAddress")
	if got := DeviceMACAddress(devices.DataModelRootDevice2, names, cache); got != "" {
		t.Errorf("MAC = %q, want empty: only BSSIDs/clients remain", got)
	}

	beacon := map[string]parameters.CachedValue{
		"Device.Ethernet.Link.2.MACAddress": {Value: "20:00:00:00:00:02"},
		"Device.Ethernet.Link.1.MACAddress": {Value: "20:00:00:00:00:01"},
	}
	if got := DeviceMACAddress(devices.DataModelRootDevice2, nil, beacon); got != "20:00:00:00:00:01" {
		t.Errorf("Beacon MAC = %q, want Ethernet.Link.1", got)
	}

	igd := map[string]parameters.CachedValue{
		"InternetGatewayDevice.WANDevice.1.WANConnectionDevice.1.WANIPConnection.1.MACAddress": {Value: "30:00:00:00:00:09"},
		"InternetGatewayDevice.LANDevice.1.LANEthernetInterfaceConfig.2.MACAddress":            {Value: "30:00:00:00:00:02"},
		"InternetGatewayDevice.LANDevice.1.LANEthernetInterfaceConfig.1.MACAddress":            {Value: "30:00:00:00:00:01"},
	}
	if got := DeviceMACAddress(devices.DataModelRootIGD1, nil, igd); got != "30:00:00:00:00:01" {
		t.Errorf("TR-098 MAC = %q, want LANEthernetInterfaceConfig.1", got)
	}
}
