package adapters

import (
	"acs/internal/devices"
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
