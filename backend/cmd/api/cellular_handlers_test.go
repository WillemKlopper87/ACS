package main

import (
	"testing"
	"time"

	"acs/internal/devices"
	"acs/internal/parameters"
)

func TestBuildCellularCapabilitiesResponseUsesDiscoveredReadOnlyPaths(t *testing.T) {
	assigned := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	device := &devices.Device{
		ID: "device-1", DataModelRoot: devices.DataModelRootDevice2,
		ProfileID: cellularStrPtr("zyxel.nr7303.eu01v1f"), ProfileMatchedBy: cellularStrPtr("model"),
		ProfileQualified: true, ProfileEvidence: []byte(`{"product_class":"NR7303-EU01V1F"}`),
		ProfileAssignedAt: &assigned,
	}
	discovery := &parameters.DiscoveredNames{
		Names: map[string]bool{
			"Device.Cellular.Interface.1.RSRP": false,
			"Device.X_ZYXEL.Cellular.Band":     false,
		},
		DiscoveredAt: assigned,
	}
	got := buildCellularCapabilitiesResponse(device, discovery, map[string]parameters.CachedValue{
		"Device.Cellular.Interface.1.RSRP": {Value: "-97", UpdatedAt: assigned, Source: parameters.SourceGetValues},
	})
	if got.Resolutions["rsrp"].Path != "Device.Cellular.Interface.1.RSRP" || !got.Resolutions["rsrp"].Standard {
		t.Fatalf("rsrp resolution = %+v, want discovered read-only standard path", got.Resolutions["rsrp"])
	}
	if got.Resolutions["band"].Path != "Device.X_ZYXEL.Cellular.Band" || !got.Resolutions["band"].Discovered {
		t.Fatalf("band resolution = %+v, want discovered vendor path", got.Resolutions["band"])
	}
	if got.Profile.ID == nil || *got.Profile.ID != "zyxel.nr7303.eu01v1f" || !got.Profile.Qualified {
		t.Fatalf("profile provenance = %+v, want qualified assigned profile", got.Profile)
	}
	if got.Values["rsrp"].Value != float64(-97) {
		t.Fatalf("normalized RSRP = %#v", got.Values["rsrp"])
	}
}

func TestBuildCellularCapabilitiesResponseDoesNotGuessTR098OrUnknownFields(t *testing.T) {
	device := &devices.Device{ID: "device-2", DataModelRoot: devices.DataModelRootIGD1}
	got := buildCellularCapabilitiesResponse(device, &parameters.DiscoveredNames{
		Names: map[string]bool{"Device.X_VENDOR.Unrelated": false},
	}, nil)
	if len(got.Resolutions) != 0 {
		t.Fatalf("resolutions = %+v, want none for TR-098 without cellular evidence", got.Resolutions)
	}
	if got.Profile.Evidence == nil {
		t.Fatal("profile evidence should be present as an empty object")
	}
}

func cellularStrPtr(value string) *string { return &value }
