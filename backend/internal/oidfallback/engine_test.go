package oidfallback

import (
	"context"
	"testing"
)

type fakeReader struct{ calls []string }

func (r *fakeReader) Read(_ context.Context, oid string) (string, bool, error) {
	r.calls = append(r.calls, oid)
	return "-900", len(r.calls) > 1, nil
}
func fixture() (Profile, Identity) {
	identity := Identity{Vendor: "MikroTik", Model: "test-model", Firmware: "test-fw", SysObjectID: "1.3.6.1.4.1.99999.1"}
	p := Profile{ID: "fixture", Vendor: identity.Vendor, Models: []string{identity.Model}, Firmwares: []string{identity.Firmware}, SysObjectID: identity.SysObjectID, Evidence: "synthetic test only", Fields: map[string]Mapping{"rsrp": {StandardPaths: []string{"Device.Cellular.Interface.1.RSRP"}, VendorPaths: []string{"Device.X_TEST.RSRP"}, OIDs: []string{"1.3.6.1.4.1.99999.2.0", "1.3.6.1.4.1.99999.3.0"}, Scale: 0.1, Unit: "dBm"}}}
	return p, identity
}
func TestFallbackPrefersDiscoveredStandardThenVendorThenOID(t *testing.T) {
	p, identity := fixture()
	reader := &fakeReader{}
	names := map[string]bool{"Device.Cellular.Interface.1.RSRP": false, "Device.X_TEST.RSRP": false}
	values := map[string]string{"Device.Cellular.Interface.1.RSRP": "-800", "Device.X_TEST.RSRP": "-1000"}
	out, err := Resolve(context.Background(), p, identity, names, values, reader)
	if err != nil || out["rsrp"].Source != "standard_parameter" || len(reader.calls) != 0 {
		t.Fatal(out, err)
	}
	delete(values, "Device.Cellular.Interface.1.RSRP")
	out, err = Resolve(context.Background(), p, identity, names, values, reader)
	if err != nil || out["rsrp"].Source != "qualified_vendor_parameter" {
		t.Fatal(out, err)
	}
	out, err = Resolve(context.Background(), p, identity, nil, nil, reader)
	if err != nil || len(reader.calls) != 2 || out["rsrp"].Value != float64(-90) {
		t.Fatal(out, err)
	}
	identity.Firmware = "different-fw"
	if _, err := Resolve(context.Background(), p, identity, nil, nil, reader); err == nil {
		t.Error("unqualified firmware accepted")
	}
}
