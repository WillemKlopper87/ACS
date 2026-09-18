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
