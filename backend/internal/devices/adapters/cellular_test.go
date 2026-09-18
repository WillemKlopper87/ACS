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
	if got := CellularPathCandidates(devices.DataModelRootDevice2, CellularIMSI, "2"); len(got) != 1 || got[0] != "Device.Cellular.Interface.2.USIM.1.IMSI" {
		t.Fatalf("IMSI candidates = %v", got)
	}
	if got := CellularPathCandidates(devices.DataModelRootDevice2, CellularCellID, "2"); got != nil {
		t.Fatalf("Cell ID must require a discovered/vendor path, got %v", got)
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
