package adapters

import (
	"acs/internal/devices"
	"strings"
	"testing"
)

func TestCellularPathCandidatesStandardDevice2(t *testing.T) {
	got := CellularPathCandidates("Device", CellularRSRP, "2")
	if len(got) != 1 || got[0] != "Device.Cellular.Interface.2.RSRP" {
		t.Fatalf("got %v", got)
	}
	if got := CellularPathCandidates("Device", CellularRSRP, ""); len(got) != 1 || got[0] != "Device.Cellular.Interface.1.RSRP" {
		t.Fatalf("default instance got %v", got)
	}
}

func TestCellularPathCandidatesDoNotPretendTR098Support(t *testing.T) {
	if got := CellularPathCandidates(devices.DataModelRootIGD1, CellularRSRP, "1"); got != nil {
		t.Fatalf("TR-098 cellular candidates must be nil, got %v", got)
	}
}

func TestCellularPathCandidatesUsePortableTR181Objects(t *testing.T) {
	if got := CellularPathCandidates(devices.DataModelRootDevice2, CellularAccessPoint, "2"); len(got) != 1 || got[0] != "Device.Cellular.AccessPoint.2.APN" {
		t.Fatalf("APN candidates = %v", got)
	}
	if got := CellularPathCandidates(devices.DataModelRootDevice2, CellularIMSI, "2"); len(got) != 2 || got[0] != "Device.Cellular.Interface.2.USIM.IMSI" {
		t.Fatalf("IMSI candidates = %v", got)
	}
	if got := CellularPathCandidates(devices.DataModelRootDevice2, CellularCellID, "2"); got != nil {
		t.Fatalf("Cell ID must require a discovered/vendor path, got %v", got)
	}
}

func TestMatchCellularStatePathMatchesStateFields(t *testing.T) {
	cases := map[string]CellularField{
		"Device.Cellular.Interface.1.Status":                  CellularStatus,
		"Device.Cellular.AccessPoint.1.APN":                   CellularAccessPoint,
		"Device.Cellular.Interface.1.USIM.1.Status":           CellularSIMStatus,
		"Device.XVendor.Cellular.NetworkName":                 CellularOperator,
		"Device.XVendor.Cellular.CellIdentity":                CellularCellID,
		"Device.Cellular.Interface.1.CurrentAccessTechnology": CellularRAT,
	}
	for path, want := range cases {
		got, ok := MatchCellularStatePath(path)
		if !ok || got != want {
			t.Fatalf("path %q: got (%v, %v), want %v", path, got, ok, want)
		}
	}
}

func TestMatchCellularStatePathDisambiguatesSharedStatusLeaf(t *testing.T) {
	got, ok := MatchCellularStatePath("Device.Cellular.Interface.1.Status")
	if !ok || got != CellularStatus {
		t.Fatalf("top-level Status: got (%v, %v), want %v", got, ok, CellularStatus)
	}
	got, ok = MatchCellularStatePath("Device.Cellular.Interface.1.USIM.1.Status")
	if !ok || got != CellularSIMStatus {
		t.Fatalf("nested USIM Status: got (%v, %v), want %v", got, ok, CellularSIMStatus)
	}
}

func TestMatchCellularStatePathExcludesContinuousTelemetry(t *testing.T) {
	for _, path := range []string{
		"Device.Cellular.Interface.1.RSSI",
		"Device.Cellular.Interface.1.RSRP",
		"Device.Cellular.Interface.1.Stats.BytesSent",
		"Device.Cellular.Interface.1.USIM.1.IMSI",
	} {
		if _, ok := MatchCellularStatePath(path); ok {
			t.Fatalf("path %q must not match a state field", path)
		}
	}
}

func TestResolveCellularReadPathAcceptsReadOnlyStandardEvidence(t *testing.T) {
	got, ok := ResolveCellularReadPath(devices.DataModelRootDevice2, CellularRSRP, "1", map[string]bool{
		"Device.Cellular.Interface.1.RSRP": false,
		"Device.XVendor.Radio.RSRP":        true,
	})
	if !ok || got.Path != "Device.Cellular.Interface.1.RSRP" || !got.Standard || got.Discovered {
		t.Fatalf("resolution = %#v, ok=%v", got, ok)
	}
}

func TestResolveCellularReadPathPrefersStandardCandidate(t *testing.T) {
	got, ok := ResolveCellularReadPath(devices.DataModelRootDevice2, CellularRSSI, "1", map[string]bool{
		"Device.Cellular.Interface.1.RSSI":    true,
		"Device.XVendor.Radio.SignalStrength": true,
	})
	if !ok || got.Path != "Device.Cellular.Interface.1.RSSI" || !got.Standard || got.Discovered {
		t.Fatalf("resolution = %#v, ok=%v", got, ok)
	}
}

func TestResolveCellularReadPathRequiresEvidenceForNonPortableField(t *testing.T) {
	if got, ok := ResolveCellularReadPath(devices.DataModelRootDevice2, CellularBand, "1", map[string]bool{
		"Device.XVendor.Radio.Unrelated": true,
	}); ok || got.Path != "" {
		t.Fatalf("unexpected resolution = %#v, ok=%v", got, ok)
	}
}

func TestResolveCellularReadPathDoesNotGuessTR098(t *testing.T) {
	if got, ok := ResolveCellularReadPath(devices.DataModelRootIGD1, CellularRSRP, "1", nil); ok || got.Path != "" {
		t.Fatalf("unexpected TR-098 resolution = %#v, ok=%v", got, ok)
	}
}

func TestNormalizeCellularValue(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want any
	}{
		{"numeric string", " -97 ", float64(-97)},
		{"text", " Registered ", "Registered"},
		{"bytes", []byte("42"), float64(42)},
		{"native", int64(7), int64(7)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeCellularValue(tc.raw)
			if got.Value != tc.want {
				t.Fatalf("value = %#v, want %#v", got.Value, tc.want)
			}
			if got.Raw == nil {
				t.Fatal("raw value was not retained")
			}
		})
	}
}

// Nokia FastMile 1.2501.00.0798 field evidence (2026-10-30).
func TestNokiaCellularFieldsResolveToFiveGCurrentLeaves(t *testing.T) {
	discovered := map[string]bool{
		"Device.Cellular.Interface.1.RSSI":                                           false,
		"Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRPCurrent":       false,
		"Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRQCurrent":       false,
		"Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.AvgRSRPMonth":      false,
		"Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.Downlink-NR-ARFCN": false,
		"Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.RSRPCurrent":      false,
		"Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.DownlinkEarfcn":   false,
	}
	want := map[CellularField]string{
		CellularRSRP:    "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRPCurrent",
		CellularRSRQ:    "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRQCurrent",
		CellularChannel: "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.Downlink-NR-ARFCN",
		CellularRSSI:    "Device.Cellular.Interface.1.RSSI",
	}
	for field, path := range want {
		got, ok := ResolveCellularReadPath("DEVICE2", field, "1", discovered)
		if !ok || got.Path != path {
			t.Errorf("%s resolved to %q (ok=%v), want %q", field, got.Path, ok, path)
		}
	}
}

func TestCellularSentinelsAreUnavailable(t *testing.T) {
	for _, raw := range []string{"-32768", "4294967295", "2147483647", " -2147483648 "} {
		if v := NormalizeCellularValue(raw); v.Value != nil {
			t.Errorf("NormalizeCellularValue(%q) = %v, want nil (no value)", raw, v.Value)
		}
	}
	if v := NormalizeCellularValue("-92"); v.Value != float64(-92) {
		t.Errorf("real reading -92 normalised to %v", v.Value)
	}
}

func TestCellularStateEventsIgnoreNonCellularStatus(t *testing.T) {
	for _, path := range []string{"Device.WiFi.SSID.1.Status", "Device.IP.Interface.2.Status", "Device.Ethernet.Link.1.Status"} {
		if field, ok := MatchCellularStatePath(path); ok {
			t.Errorf("MatchCellularStatePath(%q) = %s; non-cellular status must not raise cellular events", path, field)
		}
	}
	if _, ok := MatchCellularStatePath("Device.Cellular.Interface.1.Status"); !ok {
		t.Error("standard cellular Status must still match")
	}
}

func TestCellularDiagnosticParamsFromNamesStaysInCellularSubtree(t *testing.T) {
	names := map[string]bool{
		"Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRPCurrent":         false,
		"Device.WiFi.Radio.1.OperatingFrequencyBand":                                   true,
		"Device.WiFi.DataElements.Network.Device.1.Radio.1.BSS.1.STA.1.SignalStrength": false,
		"Device.Hosts.Host.1.X_ALU-COM_WifiRSSI":                                       false,
	}
	got := CellularDiagnosticParamsFromNames(names)
	if len(got) != 1 || got[0] != "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRPCurrent" {
		t.Fatalf("cellular refresh paths = %v, want only the cellular RSRP leaf", got)
	}
}

func TestResolveCellularReadPathFindsVendorSNR(t *testing.T) {
	const nr = "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.SNRCurrent"
	const lte = "Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.SNRCurrent"
	const avg = "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.AvgSNRMonth"

	got, ok := ResolveCellularReadPath(devices.DataModelRootDevice2, CellularSNR, "1", map[string]bool{nr: false, lte: false, avg: false})
	if !ok || got.Path != nr || !got.Discovered {
		t.Fatalf("5G preferred: got %#v ok=%v", got, ok)
	}
	got, ok = ResolveCellularReadPath(devices.DataModelRootDevice2, CellularSNR, "1", map[string]bool{lte: false, avg: false})
	if !ok || got.Path != lte || !got.Discovered {
		t.Fatalf("LTE only: got %#v ok=%v", got, ok)
	}
	if got, ok := ResolveCellularReadPath(devices.DataModelRootDevice2, CellularSNR, "1", map[string]bool{avg: false}); ok {
		t.Fatalf("aggregate must not match: got %#v", got)
	}
	if got := CellularPathCandidates(devices.DataModelRootDevice2, CellularSNR, "1"); got != nil {
		t.Fatalf("TR-181 has no SNR leaf, got %v", got)
	}
	for _, leaf := range []string{"SNR", "SINR", "SNRCurrent", "SINRCurrent"} {
		if !cellularLeafMatches(CellularSNR, leaf) {
			t.Errorf("leaf %q should match", leaf)
		}
	}
	for _, leaf := range []string{"AvgSNRMonth", "MinSNRMonth", "MaxSNRMonth"} {
		if cellularLeafMatches(CellularSNR, leaf) {
			t.Errorf("leaf %q should not match", leaf)
		}
	}
}

func TestResolveCellularReadPathsReturnsEveryMatchInPreferenceOrder(t *testing.T) {
	const p = "Device.Cellular.Interface.1."
	both := map[string]bool{
		p + "RSSI": false,
		p + "X_ALU-COM_AttachedLteCellStat.RSRPCurrent":  false,
		p + "X_ALU-COM_Attached5GCellStat.RSRPCurrent":   false,
		p + "X_ALU-COM_Attached5GCellStat.AvgRSRPMonth":  false,
		p + "X_ALU-COM_AttachedLteCellStat.MaxRSRPMonth": false,
		p + "X_ALU-COM_Attached5GCellStat.MinRSRPMonth":  false,
	}
	got := ResolveCellularReadPaths("DEVICE2", CellularRSRP, "1", both)
	want := []string{p + "X_ALU-COM_Attached5GCellStat.RSRPCurrent", p + "X_ALU-COM_AttachedLteCellStat.RSRPCurrent"}
	if len(got) != len(want) {
		t.Fatalf("both objects: %+v, want %v", got, want)
	}
	for i := range want {
		if got[i].Path != want[i] || !got[i].Discovered {
			t.Errorf("both objects [%d] = %+v, want discovered %s", i, got[i], want[i])
		}
	}
	if first, ok := ResolveCellularReadPath("DEVICE2", CellularRSRP, "1", both); !ok || first != got[0] {
		t.Errorf("ResolveCellularReadPath = %+v (ok=%v), want the first of the list %+v", first, ok, got[0])
	}

	lte := map[string]bool{p + "X_ALU-COM_AttachedLteCellStat.RSRPCurrent": false}
	if got := ResolveCellularReadPaths("DEVICE2", CellularRSRP, "1", lte); len(got) != 1 || got[0].Path != p+"X_ALU-COM_AttachedLteCellStat.RSRPCurrent" {
		t.Errorf("LTE only: %+v", got)
	}

	std := map[string]bool{p + "RSRP": false, p + "X_ALU-COM_Attached5GCellStat.RSRPCurrent": false}
	got = ResolveCellularReadPaths("DEVICE2", CellularRSRP, "1", std)
	if len(got) != 2 || !got[0].Standard || got[0].Path != p+"RSRP" || got[1].Path != p+"X_ALU-COM_Attached5GCellStat.RSRPCurrent" {
		t.Errorf("standard first: %+v", got)
	}
	if got := ResolveCellularReadPaths("DEVICE2", CellularRSRP, "1", map[string]bool{p + "Status": false}); len(got) != 0 {
		t.Errorf("no evidence: %+v", got)
	}
	if got := ResolveCellularReadPaths("DEVICE2", CellularRSRP, "1", nil); len(got) != 1 || got[0].Path != p+"RSRP" {
		t.Errorf("nil discovered: %+v", got)
	}
}

func TestResolveCellularReadPathsKeepsToTheRequestedInstance(t *testing.T) {
	const i1 = "Device.Cellular.Interface.1."
	const i2 = "Device.Cellular.Interface.2."
	names := map[string]bool{
		i1 + "RSRP": false, i2 + "RSRP": false,
		i1 + "X_ALU-COM_Attached5GCellStat.RSRPCurrent":  false,
		i2 + "X_ALU-COM_Attached5GCellStat.RSRPCurrent":  false,
		i2 + "X_ALU-COM_AttachedLteCellStat.RSRPCurrent": false,
		"Device.XVendor.Cellular.RSRP":                   false, // names no instance: kept
	}
	paths := func(instance string) []string {
		var out []string
		for _, r := range ResolveCellularReadPaths("DEVICE2", CellularRSRP, instance, names) {
			out = append(out, r.Path)
		}
		return out
	}
	want1 := []string{i1 + "RSRP", i1 + "X_ALU-COM_Attached5GCellStat.RSRPCurrent", "Device.XVendor.Cellular.RSRP"}
	got1 := paths("1")
	if len(got1) != len(want1) {
		t.Fatalf("instance 1 = %v, want %v", got1, want1)
	}
	for i := range want1 {
		if got1[i] != want1[i] {
			t.Errorf("instance 1 [%d] = %s, want %s", i, got1[i], want1[i])
		}
	}
	for _, p := range paths("2") {
		if strings.Contains(p, "Interface.1.") {
			t.Errorf("instance 2 resolved %s", p)
		}
	}
	if got := paths("2"); len(got) != 4 || got[0] != i2+"RSRP" {
		t.Errorf("instance 2 = %v", got)
	}
}
