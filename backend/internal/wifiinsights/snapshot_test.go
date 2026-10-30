package wifiinsights

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"acs/internal/parameters"
)

// fastMileNames loads the discovered tree of the field FastMile
// (ci/compatibility/discoveries, exported 2026-10-30).
func fastMileNames(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("../../../ci/compatibility/discoveries/Nokia/3TG02241/1.2501.00.0798/parameters.tsv")
	if err != nil {
		t.Skipf("discovery library not available: %v", err)
	}
	defer f.Close()
	names := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		names[cols[0]] = len(cols) > 1 && cols[1] == "RW"
	}
	return names
}

func TestReadPathsOnFastMileTree(t *testing.T) {
	paths := ReadPaths(fastMileNames(t))
	has := map[string]bool{}
	for _, p := range paths {
		has[p] = true
		if strings.HasSuffix(p, "Network.Device.") || strings.HasSuffix(p, "DataElements.") {
			t.Errorf("whole DataElements tree requested: %s", p)
		}
		if strings.Contains(p, "KeyPassphrase") || strings.Contains(p, "PreSharedKey") {
			t.Errorf("secret requested: %s", p)
		}
	}
	for _, want := range []string{
		"Device.Hosts.Host.",
		"Device.WiFi.AccessPoint.1.AssociatedDevice.",
		"Device.WiFi.AccessPoint.5.AssociatedDevice.",
		"Device.WiFi.SSID.1.Stats.BytesSent",
		"Device.WiFi.SSID.5.Stats.BytesReceived",
		"Device.WiFi.DataElements.Network.Device.2.Radio.2.BSS.1.STA.", // Beacon satellite clients
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.BSS.1.SSID",
		// Mesh node identities, so the Beacon is not counted as a client.
		"Device.WiFi.DataElements.Network.Device.2.ID",
		"Device.WiFi.DataElements.Network.Device.2.MultiAPDevice.Backhaul.MACAddress",
	} {
		if !has[want] {
			t.Errorf("missing %s", want)
		}
	}
	if len(paths) > 60 {
		t.Errorf("sample reads %d paths; keep the GPV small", len(paths))
	}
}

func cv(v string) parameters.CachedValue { return parameters.CachedValue{Value: v} }

func TestParseMergesTablesAndAttributesSSIDs(t *testing.T) {
	names := map[string]bool{
		"Device.WiFi.SSID.1.SSID": true, "Device.WiFi.SSID.1.Enable": true,
		"Device.WiFi.SSID.2.SSID": true, "Device.WiFi.SSID.2.Enable": true, "Device.WiFi.SSID.2.X_ALU-COM_IsGuestSSID": false,
		"Device.WiFi.AccessPoint.1.SSIDReference": false, "Device.WiFi.AccessPoint.2.SSIDReference": false,
	}
	cache := map[string]parameters.CachedValue{
		"Device.WiFi.SSID.1.SSID": cv("SENT-5G"), "Device.WiFi.SSID.1.Enable": cv("1"),
		"Device.WiFi.SSID.2.SSID": cv("SENT-Guest"), "Device.WiFi.SSID.2.Enable": cv("1"), "Device.WiFi.SSID.2.X_ALU-COM_IsGuestSSID": cv("1"),
		"Device.WiFi.AccessPoint.1.SSIDReference": cv("Device.WiFi.SSID.1"), "Device.WiFi.AccessPoint.2.SSIDReference": cv("Device.WiFi.SSID.2"),
	}
	values := map[string]string{
		"Device.WiFi.SSID.1.Stats.BytesSent":     "1000",
		"Device.WiFi.SSID.1.Stats.BytesReceived": "200",
		// Gateway-attached client on the primary SSID.
		"Device.WiFi.AccessPoint.1.AssociatedDevice.1.MACAddress":     "CA:76:47:8A:17:87",
		"Device.WiFi.AccessPoint.1.AssociatedDevice.1.Active":         "1",
		"Device.WiFi.AccessPoint.1.AssociatedDevice.1.SignalStrength": "-61",
		// Guest client.
		"Device.WiFi.AccessPoint.2.AssociatedDevice.1.MACAddress": "a4:fc:77:00:00:01",
		"Device.WiFi.AccessPoint.2.AssociatedDevice.1.Active":     "true",
		// The same phone in EasyMesh (byte counters) and Hosts (name).
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.BSS.1.SSID":                 "SENT-5G",
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.BSS.1.STA.1.MACAddress":     "ca:76:47:8a:17:87",
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.BSS.1.STA.1.BytesSent":      "5000",
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.BSS.1.STA.1.BytesReceived":  "700",
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.BSS.1.STA.1.SignalStrength": "100",
		// A client behind the Beacon (mesh node 2), only visible in DataElements.
		"Device.WiFi.DataElements.Network.Device.2.Radio.2.BSS.1.SSID":                 "SENT-5G",
		"Device.WiFi.DataElements.Network.Device.2.Radio.2.BSS.1.STA.4.MACAddress":     "3a:27:5f:56:0b:f8",
		"Device.WiFi.DataElements.Network.Device.2.Radio.2.BSS.1.STA.4.SignalStrength": "120",
		"Device.Hosts.Host.1.PhysAddress":                                              "CA:76:47:8A:17:87",
		"Device.Hosts.Host.1.HostName":                                                 "Galaxy-A12",
		"Device.Hosts.Host.1.Active":                                                   "1",
		"Device.Hosts.Host.2.PhysAddress":                                              "ec:74:d7:1e:89:84",
		"Device.Hosts.Host.2.InterfaceType":                                            "Ethernet",
		"Device.Hosts.Host.2.Active":                                                   "1",
		"Device.Hosts.Host.3.PhysAddress":                                              "ec:74:d7:00:00:09",
		"Device.Hosts.Host.3.Active":                                                   "0", // gone: ignored
	}
	snap := Parse(values, names, cache)

	if len(snap.SSIDs) != 1 || snap.SSIDs[0].Name != "SENT-5G" || snap.SSIDs[0].Role != "primary" || snap.SSIDs[0].BytesDown != 1000 || snap.SSIDs[0].BytesUp != 200 {
		t.Fatalf("ssids = %+v", snap.SSIDs)
	}
	byMAC := map[string]Client{}
	for _, c := range snap.Clients {
		byMAC[c.MAC] = c
	}
	if len(byMAC) != 4 {
		t.Fatalf("clients = %+v", snap.Clients)
	}
	phone := byMAC["ca:76:47:8a:17:87"]
	if phone.Hostname != "Galaxy-A12" || phone.SSID != 1 || !phone.Wireless || phone.Guest || phone.SignalDBm == nil || *phone.SignalDBm != -61 {
		t.Errorf("phone = %+v", phone)
	}
	if phone.BytesDown == nil || *phone.BytesDown != 5000 || phone.BytesUp == nil || *phone.BytesUp != 700 {
		t.Errorf("phone counters = %v/%v", phone.BytesDown, phone.BytesUp)
	}
	if g := byMAC["a4:fc:77:00:00:01"]; !g.Guest || g.SSID != 2 {
		t.Errorf("guest = %+v", g)
	}
	beacon := byMAC["3a:27:5f:56:0b:f8"]
	if beacon.MeshNode != 2 || beacon.SSID != 1 || beacon.SignalDBm == nil || *beacon.SignalDBm != -50 {
		t.Errorf("beacon client = %+v (signal %v)", beacon, beacon.SignalDBm)
	}
	if wired := byMAC["ec:74:d7:1e:89:84"]; wired.Wireless {
		t.Errorf("wired host marked wireless: %+v", wired)
	}
	wireless, wired, guest := snap.Counts()
	if wireless != 3 || wired != 1 || guest != 1 {
		t.Errorf("counts = %d/%d/%d", wireless, wired, guest)
	}
}

func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"CA:76:47:8A:17:87": "ca:76:47:8a:17:87",
		"ca-76-47-8a-17-87": "ca:76:47:8a:17:87",
		"ca76.478a.1787":    "ca:76:47:8a:17:87",
		"00:00:00:00:00:00": "",
		"":                  "",
		"not-a-mac":         "",
	}
	for in, want := range cases {
		if got := NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVendorAndPrivateMAC(t *testing.T) {
	reg, err := LoadOUIRegistry(strings.NewReader("Registry,Assignment,Organization Name,Organization Address\nMA-L,A4FC77,Nokia Solutions and Networks,Espoo\nMA-L,EC74D7,Grandstream Networks Inc,Boston\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v := reg.Vendor("a4:fc:77:00:00:01"); v != "Nokia Solutions and Networks" {
		t.Errorf("vendor = %q", v)
	}
	// All five GDMS top clients in the field screenshot use private MACs.
	for _, mac := range []string{"ca:76:47:8a:17:87", "1e:05:21:d9:93:38", "5e:a9:52:24:58:36", "9a:b5:36:c4:cd:6e", "3a:27:5f:56:0b:f8"} {
		if !PrivateMAC(mac) || reg.Vendor(mac) != "Private" {
			t.Errorf("%s not reported private", mac)
		}
	}
	if PrivateMAC("ec:74:d7:1e:89:84") {
		t.Error("global MAC reported private")
	}
	var none *OUIRegistry
	if none.Vendor("a4:fc:77:00:00:01") != "" {
		t.Error("nil registry should report unknown")
	}
}

func TestInferOS(t *testing.T) {
	cases := map[string]string{
		"Galaxy-A12": "Android", "Redmi-A3": "Android", "android-1a2b": "Android",
		"Willems-iPhone": "iOS", "iPad": "iOS", "DESKTOP-AB12": "Windows", "": "Other", "SaveMore": "Other",
	}
	for host, want := range cases {
		if got := InferOS(host); got != want {
			t.Errorf("InferOS(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestSummarise(t *testing.T) {
	stats, vendors, oses := summarise([]ClientSummary{
		{MAC: "a", Vendor: "Private", PrivateMAC: true, Wireless: true, OSInferred: "Android", Sessions: 2, ConnectedSeconds: 1200, FirstSeenAt: mustTime("2026-10-30T10:00:00Z")},
		{MAC: "b", Vendor: "", Wireless: false, OSInferred: "Other", Sessions: 1, ConnectedSeconds: 600, FirstSeenAt: mustTime("2026-10-29T10:00:00Z")},
	}, mustTime("2026-10-30T08:00:00Z"))
	if stats.New != 1 || stats.Returning != 1 || stats.Private != 1 || stats.AverageSessionSeconds != 600 {
		t.Errorf("stats = %+v", stats)
	}
	if len(vendors) != 2 || len(oses) != 1 || oses[0].Name != "Android" {
		t.Errorf("vendors=%v oses=%v", vendors, oses)
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestParseExcludesMeshNodes: field data 2026-10-30. The Beacon showed up
// as two "clients" of the FastMile: its backhaul STA on the hidden backhaul
// BSS (hostname "Nokia WiFi Beacon 3.1") and a Hosts entry for its own MAC.
// EasyMesh Network.Device.{n}.ID and MultiAPDevice.Backhaul.MACAddress
// name both, so neither may count as a client.
func TestParseExcludesMeshNodes(t *testing.T) {
	sta := "Device.WiFi.DataElements.Network.Device.1.Radio.2.BSS.1.STA."
	values := map[string]string{
		"Device.WiFi.DataElements.Network.Device.1.ID":                                "a4:fc:77:00:00:01",
		"Device.WiFi.DataElements.Network.Device.2.ID":                                "BC:51:5F:5E:7C:D3",
		"Device.WiFi.DataElements.Network.Device.2.MultiAPDevice.Backhaul.MACAddress": "D6-51-5F-5E-7C-DD",
		sta + "1.MACAddress":                "d6:51:5f:5e:7c:dd",
		sta + "1.Hostname":                  "Nokia WiFi Beacon 3.1",
		sta + "1.BytesSent":                 "4000000",
		sta + "2.MACAddress":                "32:a9:5c:44:3e:9a",
		sta + "2.Hostname":                  "HUAWEI_P40_Pro",
		"Device.Hosts.Host.1.PhysAddress":   "bc:51:5f:5e:7c:d3",
		"Device.Hosts.Host.1.Active":        "1",
		"Device.Hosts.Host.1.InterfaceType": "802.11",
		"Device.Hosts.Host.2.PhysAddress":   "32:a9:5c:44:3e:9a",
		"Device.Hosts.Host.2.Active":        "1",
	}
	snap := Parse(values, nil, nil)
	if len(snap.Clients) != 1 || snap.Clients[0].MAC != "32:a9:5c:44:3e:9a" {
		t.Fatalf("clients = %+v, want only the phone", snap.Clients)
	}
	if w, _, _ := snap.Counts(); w != 1 {
		t.Errorf("wireless count = %d, want 1", w)
	}
	want := map[string]bool{"a4:fc:77:00:00:01": true, "bc:51:5f:5e:7c:d3": true, "d6:51:5f:5e:7c:dd": true}
	if len(snap.MeshMACs) != len(want) {
		t.Fatalf("MeshMACs = %v, want %v", snap.MeshMACs, want)
	}
	for _, m := range snap.MeshMACs {
		if !want[m] {
			t.Errorf("unexpected mesh MAC %s", m)
		}
	}
}

// TestExcludeMACsAndSatelliteMACs: a linked satellite's own interface
// MACs (from its parameter cache) are excluded too, for a mesh node the
// gateway lists under a MAC its EasyMesh table does not carry.
func TestExcludeMACsAndSatelliteMACs(t *testing.T) {
	beaconCache := map[string]parameters.CachedValue{
		"Device.Ethernet.Link.1.MACAddress": cv("BC:51:5F:5E:7C:D0"),
		"Device.Ethernet.Link.2.MACAddress": cv(""),
		"Device.DeviceInfo.SerialNumber":    cv("ALCLB499A341"),
	}
	macs := SatelliteMACs(beaconCache)
	if len(macs) != 1 || macs[0] != "bc:51:5f:5e:7c:d0" {
		t.Fatalf("SatelliteMACs = %v", macs)
	}
	snap := Snapshot{Clients: []Client{{MAC: "bc:51:5f:5e:7c:d0", Wireless: false}, {MAC: "32:a9:5c:44:3e:9a", Wireless: true}}}
	snap.ExcludeMACs(macs)
	if len(snap.Clients) != 1 || snap.Clients[0].MAC != "32:a9:5c:44:3e:9a" {
		t.Fatalf("clients after exclude = %+v", snap.Clients)
	}
	if len(snap.MeshMACs) != 1 || snap.MeshMACs[0] != "bc:51:5f:5e:7c:d0" {
		t.Errorf("MeshMACs = %v", snap.MeshMACs)
	}
	snap.ExcludeMACs(macs) // idempotent
	if len(snap.MeshMACs) != 1 {
		t.Errorf("MeshMACs duplicated: %v", snap.MeshMACs)
	}
}
