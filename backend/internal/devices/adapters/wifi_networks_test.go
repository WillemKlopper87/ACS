package adapters

import (
	"reflect"
	"testing"

	"acs/internal/parameters"
)

// A FastMile-shaped EasyMesh controller: SSID.1/2 are the 2.4/5 GHz
// fronthaul, SSID.3 is a vendor-flagged guest SSID, SSID.4 is bound to a
// guest WLAN, SSID.5 has no AccessPoint (backhaul).
func fastMileWiFi() (map[string]bool, map[string]parameters.CachedValue) {
	names := map[string]bool{}
	cache := map[string]parameters.CachedValue{}
	set := func(path string, writable bool, value string) {
		names[path] = writable
		if value != "\x00" {
			cache[path] = parameters.CachedValue{Value: value}
		}
	}
	for i, band := range map[string]string{"1": "2.4GHz", "2": "5GHz"} {
		set("Device.WiFi.Radio."+i+".OperatingFrequencyBand", true, band)
	}
	for _, s := range []struct{ inst, name, radio, guest string }{
		{"1", "Home", "Device.WiFi.Radio.1", "0"}, {"2", "Home", "Device.WiFi.Radio.2", "0"},
		{"3", "Visitors", "Device.WiFi.Radio.1", "1"}, {"4", "Party", "Device.WiFi.Radio.2", "0"},
		{"5", "mesh-bh", "Device.WiFi.Radio.2", "0"},
	} {
		p := "Device.WiFi.SSID." + s.inst + "."
		set(p+"SSID", true, s.name)
		set(p+"Enable", true, "1")
		set(p+"LowerLayers", true, s.radio)
		set(p+"X_ALU-COM_IsGuestSSID", true, s.guest)
	}
	for _, ap := range []string{"1", "2", "3", "4"} {
		p := "Device.WiFi.AccessPoint." + ap + "."
		set(p+"SSIDReference", true, "Device.WiFi.SSID."+ap)
		set(p+"Security.KeyPassphrase", true, "\x00") // never cached
		set(p+"Security.ModeEnabled", true, "WPA2-WPA3-Personal")
		set(p+"SSIDAdvertisementEnabled", true, "1")
	}
	set("Device.WiFi.X_ALU-COM_WLANForGuest.1.SSIDReference", true, "Device.WiFi.SSID.4.")
	return names, cache
}

func TestResolveWiFiNetworksRoles(t *testing.T) {
	names, cache := fastMileWiFi()
	got := ResolveWiFiNetworks(names, cache)
	if !got.Complete {
		t.Fatalf("expected complete view, missing %v", got.RefreshPaths)
	}
	roles := map[int]string{}
	for _, n := range got.Networks {
		roles[n.Instance] = n.Role
	}
	want := map[int]string{1: WiFiRolePrimary, 2: WiFiRolePrimary, 3: WiFiRoleGuest, 4: WiFiRoleGuest, 5: WiFiRoleUnbound}
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	primary := PrimaryWiFiNetworks(got)
	if len(primary) != 2 || primary[0].Band != "2.4GHz" || primary[1].Band != "5GHz" {
		t.Fatalf("primary = %+v, want SSID.1 (2.4GHz) and SSID.2 (5GHz)", primary)
	}
	if primary[1].PassphrasePath != "Device.WiFi.AccessPoint.2.Security.KeyPassphrase" || !primary[1].PassphraseWrite {
		t.Fatalf("passphrase path = %q writable=%v", primary[1].PassphrasePath, primary[1].PassphraseWrite)
	}
}

func TestResolveWiFiNetworksIncompleteWithoutCachedValues(t *testing.T) {
	names, _ := fastMileWiFi()
	got := ResolveWiFiNetworks(names, map[string]parameters.CachedValue{})
	if got.Complete || len(got.RefreshPaths) == 0 {
		t.Fatalf("expected an incomplete view with refresh paths, got %+v", got)
	}
	for _, p := range got.RefreshPaths {
		if p == "Device.WiFi.AccessPoint.1.Security.KeyPassphrase" {
			t.Fatal("refresh paths must never include secrets")
		}
	}
	for _, n := range got.Networks {
		if n.Instance == 1 && !n.APInferred {
			t.Fatal("AccessPoint pairing without a cached SSIDReference must be flagged as inferred")
		}
	}
}

func TestWiFiNetworkReadPathsExcludeSecretsAndClients(t *testing.T) {
	names, _ := fastMileWiFi()
	names["Device.WiFi.AccessPoint.1.AssociatedDevice.1.MACAddress"] = false
	for _, p := range WiFiNetworkReadPaths(names) {
		if p == "Device.WiFi.AccessPoint.1.Security.KeyPassphrase" || p == "Device.WiFi.AccessPoint.1.AssociatedDevice.1.MACAddress" {
			t.Fatalf("read paths include %q", p)
		}
	}
}
