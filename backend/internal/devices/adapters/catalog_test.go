package adapters

import "testing"

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

func TestLoadCatalogsParsesEmbeddedVendorFiles(t *testing.T) {
	catalogs := LoadCatalogs()

	for _, vendor := range []string{"huawei", "nokia", "zyxel", "teltonika"} {
		cat, ok := catalogs[vendor]
		if !ok {
			t.Errorf("expected catalog for vendor %q", vendor)
			continue
		}
		if len(cat.Parameters) == 0 {
			t.Errorf("vendor %q catalog has no parameters", vendor)
		}
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
