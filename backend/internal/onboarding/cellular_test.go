package onboarding

import (
	"reflect"
	"testing"

	"acs/internal/parameters"
)

// The FastMile 1.2501.00.0798 discovered tree: all of these are writable.
var fastMileCellular = map[string]bool{
	"Device.Cellular.AccessPoint.1.APN":                     true,
	"Device.Cellular.AccessPoint.1.X_ALU-COM_DNN":           true,
	"Device.Cellular.AccessPoint.1.X_ALU-COM_APNMTU":        true,
	"Device.Cellular.AccessPoint.2.APN":                     true,
	"Device.Cellular.Interface.1.X_ALU-COM_APNForBootstrap": true,
	"Device.Cellular.Interface.1.USIM.IMSI":                 false,
	"Device.Cellular.Interface.1.USIM.ICCID":                false,
}

func TestCellularWrites(t *testing.T) {
	cache := map[string]parameters.CachedValue{
		"Device.Cellular.AccessPoint.1.APN":           {Value: "internet"}, // already right: not rewritten
		"Device.Cellular.AccessPoint.1.X_ALU-COM_DNN": {Value: "old"},
	}
	got := CellularWrites(CellularProfile{APN: "internet", DNN: "internet", MTU: 1420, BootstrapAPN: "boot"}, fastMileCellular, cache)
	want := []Write{
		{Name: "Device.Cellular.AccessPoint.1.X_ALU-COM_DNN", Value: "internet", Type: "xsd:string"},
		{Name: "Device.Cellular.AccessPoint.1.X_ALU-COM_APNMTU", Value: "1420", Type: "xsd:unsignedInt"},
		{Name: "Device.Cellular.Interface.1.X_ALU-COM_APNForBootstrap", Value: "boot", Type: "xsd:string"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("writes = %+v\nwant %+v", got, want)
	}
}

func TestCellularWritesSkipsWhatTheDeviceLacksOrCannotWrite(t *testing.T) {
	names := map[string]bool{"Device.Cellular.AccessPoint.1.APN": false} // read-only
	if got := CellularWrites(CellularProfile{APN: "internet", DNN: "x"}, names, nil); len(got) != 0 {
		t.Fatalf("writes = %+v, want none", got)
	}
	if got := CellularWrites(CellularProfile{}, fastMileCellular, nil); len(got) != 0 {
		t.Fatalf("empty profile wrote %+v", got)
	}
}

func TestSIMIdentifiers(t *testing.T) {
	cache := map[string]parameters.CachedValue{
		"Device.Cellular.Interface.1.USIM.ICCID": {Value: "89270000000000001234"},
		"Device.Cellular.Interface.1.USIM.IMSI":  {Value: "655060000000013"},
	}
	iccid, imsi := SIMIdentifiers(cache)
	if iccid != "89270000000000001234" || imsi != "655060000000013" {
		t.Fatalf("got %q %q", iccid, imsi)
	}
	paths := SIMReadPaths(fastMileCellular)
	if !reflect.DeepEqual(paths, []string{"Device.Cellular.Interface.1.USIM.ICCID", "Device.Cellular.Interface.1.USIM.IMSI"}) {
		t.Fatalf("read paths = %v", paths)
	}
	if paths := SIMReadPaths(nil); len(paths) != 2 {
		t.Fatalf("undiscovered device read paths = %v, want the TR-181 defaults", paths)
	}
}
