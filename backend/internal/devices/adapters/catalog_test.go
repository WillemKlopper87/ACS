package adapters

import (
	"strings"
	"testing"
)

const sampleXML = `<?xml version="1.0" encoding="UTF-8"?>
<cwmp:cwmpDataModel vendor="Acme" model="Widget 5000" specVersion="TR-181 i2">
  <object name="Device." access="readOnly">
    <object name="DeviceInfo." access="readOnly">
      <parameter name="SoftwareVersion" type="string" access="readOnly"/>
    </object>
    <object name="X_ACME_Cellular." access="readOnly">
      <parameter name="RSRP" type="int" access="readOnly"/>
      <parameter name="APNName" type="string" access="readWrite"/>
    </object>
  </object>
</cwmp:cwmpDataModel>
`

func TestParseCatalogFlattensPaths(t *testing.T) {
	cat, err := parseCatalog([]byte(sampleXML))
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	if cat.Vendor != "Acme" || cat.Model != "Widget 5000" {
		t.Errorf("Vendor/Model = %q/%q, want Acme/Widget 5000", cat.Vendor, cat.Model)
	}
	if len(cat.Parameters) != 3 {
		t.Fatalf("Parameters len = %d, want 3: %+v", len(cat.Parameters), cat.Parameters)
	}

	want := map[string]string{
		"Device.DeviceInfo.SoftwareVersion": "readOnly",
		"Device.X_ACME_Cellular.RSRP":       "readOnly",
		"Device.X_ACME_Cellular.APNName":    "readWrite",
	}
	for _, p := range cat.Parameters {
		access, ok := want[p.Path]
		if !ok {
			t.Errorf("unexpected path %q", p.Path)
			continue
		}
		if p.Access != access {
			t.Errorf("Path %q Access = %q, want %q", p.Path, p.Access, access)
		}
		delete(want, p.Path)
	}
	for missing := range want {
		t.Errorf("expected path %q not found", missing)
	}
}

func TestCellularDiagnosticParamsFiltersReadOnlyRFParams(t *testing.T) {
	cat, err := parseCatalog([]byte(sampleXML))
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}

	got := cat.CellularDiagnosticParams()
	if len(got) != 1 || got[0] != "Device.X_ACME_Cellular.RSRP" {
		t.Errorf("CellularDiagnosticParams() = %v, want [Device.X_ACME_Cellular.RSRP]", got)
	}
}

func TestCellularDiagnosticParamsFromNamesUsesLiveDeviceTree(t *testing.T) {
	got := CellularDiagnosticParamsFromNames(map[string]bool{
		"Device.Cellular.Interface.1.RSRP":  false,
		"Device.X_ZYXEL_Cellular.NR5G_Band": false,
		"Device.WiFi.SSID.1.SSID":           true,
		"Device.Cellular.Interface.1.IMEI":  false,
	})
	want := []string{"Device.Cellular.Interface.1.RSRP", "Device.X_ZYXEL_Cellular.NR5G_Band"}
	if len(got) != len(want) {
		t.Fatalf("diagnostic paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("diagnostic paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadCatalogsParsesEmbeddedVendorFiles(t *testing.T) {
	catalogs := LoadCatalogs()

	for _, vendor := range []string{"huawei", "nokia", "zyxel", "teltonika", "zte", "tplink"} {
		cat, ok := catalogs[vendor]
		if !ok {
			t.Errorf("expected catalog for vendor %q", vendor)
			continue
		}
		if len(cat.Parameters) == 0 {
			t.Errorf("vendor %q catalog has no parameters", vendor)
		}
	}
	if got := catalogs["zyxel"].Model; got != "NR7303-EU01V1F" {
		t.Errorf("Zyxel catalog model = %q, want NR7303-EU01V1F", got)
	}
}

func TestRegistryMatchCellularDiagnostics(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		manufacturer string
		wantVendor   string
	}{
		{"Zyxel Communications Corp.", "Zyxel"},
		{"HUAWEI TECHNOLOGIES CO.,LTD", "Huawei"},
		{"Nokia", "Nokia"},
		{"Teltonika Networks", "Teltonika"},
		{"ZTE Corporation", "ZTE"},
		{"TP Link Technologies Co., Ltd.", "TP-Link"},
	}
	for _, tt := range tests {
		vendor, paths := r.MatchCellularDiagnostics(tt.manufacturer)
		if vendor != tt.wantVendor {
			t.Errorf("manufacturer %q matched vendor %q, want %q", tt.manufacturer, vendor, tt.wantVendor)
		}
		if len(paths) == 0 {
			t.Errorf("manufacturer %q returned no diagnostic paths", tt.manufacturer)
		}
	}
}

func TestRegistryMatchCellularDiagnosticsFallback(t *testing.T) {
	r := NewRegistry()

	vendor, paths := r.MatchCellularDiagnostics("Some Unknown Vendor Inc.")
	if vendor != "" {
		t.Errorf("expected no vendor match, got %q", vendor)
	}
	if len(paths) != len(genericCellularFallback) {
		t.Errorf("expected generic fallback paths, got %v", paths)
	}
}

func TestRegistryMatchesQualifiedModelBeforeVendorFallback(t *testing.T) {
	r := NewRegistry()

	vendor, paths, qualified := r.MatchCellularDiagnosticsForDevice(
		"Zyxel Communications Corp.", "NR7303-EU01V1F")
	if vendor != "Zyxel" || qualified {
		t.Fatalf("model-only match = vendor %q qualified %v, want Zyxel/false without firmware evidence", vendor, qualified)
	}
	if len(paths) == 0 {
		t.Fatal("qualified profile returned no diagnostic paths")
	}

	// A different Zyxel ProductClass must not be silently treated as the
	// NR7303 pack. It remains eligible only for the conservative vendor
	// baseline until discovery or a dedicated profile qualifies it.
	vendor, _, qualified = r.MatchCellularDiagnosticsForDevice(
		"Zyxel Communications Corp.", "NR5103")
	if vendor != "Zyxel" || qualified {
		t.Fatalf("vendor fallback = vendor %q qualified %v, want Zyxel/false", vendor, qualified)
	}
}

func TestProfileIDIsStableFromCatalogMetadata(t *testing.T) {
	if got := ProfileID(Catalog{Vendor: "Zyxel", Model: "NR7303-EU01V1F"}); got != "zyxel.nr7303eu01v1f" {
		t.Fatalf("ProfileID = %q, want zyxel.nr7303eu01v1f", got)
	}
	if got := ProfileID(Catalog{Vendor: "TP-Link"}); got != "tplink" {
		t.Fatalf("vendor-only ProfileID = %q, want tplink", got)
	}
}

func TestFirmwareProfileRequiresEvidenceAndSelectsVariant(t *testing.T) {
	r := &Registry{catalogs: []Catalog{
		{Vendor: "Example", Model: "5G-CPE"},
		{Vendor: "Example", Model: "5G-CPE", Firmwares: []string{"2.0"}, QualificationEvidence: "lab/report-2.0"},
	}}
	if match := r.MatchFirmwareProfile("Example Inc", "5G-CPE", "2.0"); !match.Qualified || match.Catalog.QualificationEvidence != "lab/report-2.0" {
		t.Fatalf("variant not selected: %+v", match)
	}
	for _, version := range []string{"", "1.0", "2.0-beta"} {
		if r.MatchFirmwareProfile("Example Inc", "5G-CPE", version).Qualified {
			t.Fatalf("unqualified firmware %q accepted", version)
		}
	}
	r.catalogs[1].QualificationEvidence = ""
	if r.MatchFirmwareProfile("Example Inc", "5G-CPE", "2.0").Qualified {
		t.Fatal("missing test evidence accepted")
	}
}

// Field test 2026-10-30: the FastMile 5G Gateway 2 reports ProductClass
// 3TG02241, and the Beacon 3.1 mesh unit reports Manufacturer "ALCL".
func TestNokiaFieldDevicesMatchByModel(t *testing.T) {
	r := NewRegistry()
	for _, tc := range []struct{ manufacturer, productClass, wantVendor string }{
		{"Nokia", "3TG02241", "Nokia"},
		{"ALCL", "Beacon 3.1", "ALCL"},
	} {
		m := r.MatchProfile(tc.manufacturer, tc.productClass)
		if m.MatchedBy != "model" || m.Catalog.Vendor != tc.wantVendor {
			t.Errorf("MatchProfile(%q, %q) = vendor %q by %q, want %q by model", tc.manufacturer, tc.productClass, m.Catalog.Vendor, m.MatchedBy, tc.wantVendor)
		}
	}

	_, paths := r.MatchCellularDiagnostics("Nokia")
	for _, p := range paths {
		if strings.Contains(p, "X_NOKIA_") {
			t.Errorf("Nokia diagnostics include %q; firmware 1.2501.00.0798 uses X_ALU-COM_", p)
		}
	}
	want := "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRPCurrent"
	found := false
	for _, p := range paths {
		found = found || p == want
	}
	if !found {
		t.Errorf("Nokia diagnostics %v missing %s", paths, want)
	}
}
