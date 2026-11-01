package main

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/parameters"
)

// fastMileWiFiTree is the field layout of the Nokia FastMile 5G Gateway 2
// (2026-10-30): SSID.1 (2.4 GHz) and SSID.5 (5 GHz) are the primary
// "SENT-5G" network, SSID.2/6 the guest pair (X_ALU-COM_IsGuestSSID=1),
// SSID.3/4/7/8 disabled factory SSIDs.
func fastMileWiFiTree() (map[string]bool, map[string]parameters.CachedValue) {
	names := map[string]bool{}
	cache := map[string]parameters.CachedValue{}
	set := func(path string, writable bool, value *string) {
		names[path] = writable
		if value != nil {
			cache[path] = parameters.CachedValue{Value: *value}
		}
	}
	val := func(s string) *string { return &s }
	set("Device.WiFi.Radio.1.OperatingFrequencyBand", false, val("2.4GHz"))
	set("Device.WiFi.Radio.2.OperatingFrequencyBand", false, val("5GHz"))
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
		set(p+"SSID", true, val(name))
		set(p+"Enable", true, val(enable))
		set(p+"LowerLayers", true, val(radio))
		set(p+"X_ALU-COM_IsGuestSSID", true, val(guest))
		set(p+"MACAddress", false, val("aa:bb:cc:dd:ee:0"+inst))
		ap := "Device.WiFi.AccessPoint." + inst + "."
		set(ap+"SSIDReference", true, val("Device.WiFi.SSID."+inst))
		set(ap+"Security.KeyPassphrase", true, nil) // never read back
	}
	return names, cache
}

func TestPlanWiFiWritesFastMileWritesBothPrimaryBands(t *testing.T) {
	names, cache := fastMileWiFiTree()
	off := false
	plan, err := planWiFiWrites(devices.DataModelRootDevice2, names, cache,
		wifiSettings{SSID: "NewHome", Password: "correct-horse", Enabled: &off})
	if err != nil {
		t.Fatalf("planWiFiWrites: %v", err)
	}
	want := []jobs.ParameterWrite{
		{Name: "Device.WiFi.SSID.1.SSID", Value: "NewHome", Type: "xsd:string"},
		{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: "correct-horse", Type: "xsd:string"},
		{Name: "Device.WiFi.SSID.1.Enable", Value: "0", Type: "xsd:boolean"},
		{Name: "Device.WiFi.SSID.5.SSID", Value: "NewHome", Type: "xsd:string"},
		{Name: "Device.WiFi.AccessPoint.5.Security.KeyPassphrase", Value: "correct-horse", Type: "xsd:string"},
		{Name: "Device.WiFi.SSID.5.Enable", Value: "0", Type: "xsd:boolean"},
	}
	if !reflect.DeepEqual(plan.Payload.Parameters, want) {
		t.Fatalf("writes = %+v\nwant %+v", plan.Payload.Parameters, want)
	}
	if !plan.Payload.Verify {
		t.Error("SET_WIFI job must be verified (read-back)")
	}
	if plan.Detail != "SSID 1 (2.4GHz) + SSID 5 (5GHz)" {
		t.Errorf("detail = %q", plan.Detail)
	}
}

func TestPlanWiFiWritesIncompleteViewAsksForRead(t *testing.T) {
	names, cache := fastMileWiFiTree()
	delete(cache, "Device.WiFi.SSID.2.X_ALU-COM_IsGuestSSID")
	plan, err := planWiFiWrites(devices.DataModelRootDevice2, names, cache, wifiSettings{SSID: "NewHome"})
	if !errors.Is(err, errWiFiRolesUnknown) {
		t.Fatalf("err = %v, want errWiFiRolesUnknown", err)
	}
	if !reflect.DeepEqual(plan.RefreshPaths, []string{"Device.WiFi.SSID.2.X_ALU-COM_IsGuestSSID"}) {
		t.Errorf("refresh paths = %v", plan.RefreshPaths)
	}
	if len(plan.Payload.Parameters) != 0 {
		t.Errorf("no write may be planned while roles are unknown: %+v", plan.Payload.Parameters)
	}
}

func TestPlanWiFiWritesRefusals(t *testing.T) {
	names, cache := fastMileWiFiTree()
	noPrimary := map[string]parameters.CachedValue{}
	for k, v := range cache {
		if strings.HasSuffix(k, ".Enable") {
			v.Value = "0"
		}
		noPrimary[k] = v
	}
	readOnly := map[string]bool{}
	for k, v := range names {
		readOnly[k] = v && k != "Device.WiFi.SSID.5.SSID"
	}
	beacon := map[string]bool{"Device.DeviceInfo.SerialNumber": false} // Wi-Fi run by the gateway
	for _, tc := range []struct {
		name  string
		root  string
		names map[string]bool
		cache map[string]parameters.CachedValue
		want  string
	}{
		{"never discovered", devices.DataModelRootDevice2, nil, cache, "discovery"},
		{"no Wi-Fi tree", devices.DataModelRootDevice2, beacon, nil, "no Device.WiFi.SSID"},
		{"no primary", devices.DataModelRootDevice2, names, noPrimary, "no primary"},
		{"read-only name", devices.DataModelRootDevice2, readOnly, cache, "network 5 is not writable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planWiFiWrites(tc.root, tc.names, tc.cache, wifiSettings{SSID: "NewHome"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestPlanWiFiWritesTR098KeepsCanonicalPath(t *testing.T) {
	plan, err := planWiFiWrites(devices.DataModelRootIGD1, nil, nil, wifiSettings{SSID: "NewHome"})
	if err != nil {
		t.Fatalf("planWiFiWrites: %v", err)
	}
	want := []jobs.ParameterWrite{{Name: "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.SSID", Value: "NewHome", Type: "xsd:string"}}
	if !reflect.DeepEqual(plan.Payload.Parameters, want) {
		t.Fatalf("writes = %+v, want %+v", plan.Payload.Parameters, want)
	}
	// With discovery evidence, a read-only canonical path is refused.
	_, err = planWiFiWrites(devices.DataModelRootIGD1,
		map[string]bool{"InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.SSID": false}, nil, wifiSettings{SSID: "NewHome"})
	if err == nil {
		t.Fatal("read-only TR-098 SSID must be refused")
	}
}

func TestWiFiSettingsValidate(t *testing.T) {
	on := true
	for _, tc := range []struct {
		in wifiSettings
		ok bool
	}{
		{wifiSettings{}, false},
		{wifiSettings{SSID: "Home"}, true},
		{wifiSettings{SSID: " Home"}, false},
		{wifiSettings{SSID: strings.Repeat("x", 33)}, false},
		{wifiSettings{Password: "short"}, false},
		{wifiSettings{Password: "long-enough"}, true},
		{wifiSettings{Enabled: &on}, true},
	} {
		if err := tc.in.validate(); (err == nil) != tc.ok {
			t.Errorf("validate(%+v) = %v, want ok=%v", tc.in, err, tc.ok)
		}
	}
}
