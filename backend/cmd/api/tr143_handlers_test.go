package main

import (
	"testing"
	"time"

	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/parameters"
)

func TestDiagnosticsCapabilitiesResponseShape(t *testing.T) {
	// Keep this contract test close to the API surface: a discovered object is
	// enough to advertise support, while a legacy-only path is ignored.
	names := map[string]bool{
		"Device.IP.Diagnostics.DownloadDiagnostics.": false,
		"Device.IP.Diagnostics.UploadDiagnostics.":   false,
	}
	capability := adapters.ResolveTR143Capability(names)
	if !capability.DownloadSupported() || !capability.UploadSupported() {
		t.Fatalf("capability = %+v", capability)
	}
	if got := discoveredAtString(&parameters.DiscoveredNames{DiscoveredAt: time.Unix(1, 0)}); got == nil {
		t.Fatal("discovery timestamp should be returned")
	}
	if devices.DataModelRootDevice2 == "" {
		t.Fatal("device root constant unexpectedly empty")
	}
}
