package adapters

import "testing"

func TestResolveTR143CapabilityRequiresDiscoveredObjects(t *testing.T) {
	got := ResolveTR143Capability(map[string]bool{
		"Device.IP.Diagnostics.DownloadDiagnostics.": false,
		"Device.IP.Diagnostics.UploadDiagnostics":    false,
		"InternetGatewayDevice.DownloadDiagnostics.": true,
	})
	if !got.DownloadSupported() || !got.UploadSupported() {
		t.Fatalf("capability = %+v", got)
	}
	if got.DownloadPath != "Device.IP.Diagnostics.DownloadDiagnostics." || got.UploadPath != "Device.IP.Diagnostics.UploadDiagnostics." {
		t.Fatalf("paths = %+v", got)
	}
}

func TestResolveTR143CapabilityDoesNotGuessLegacyPaths(t *testing.T) {
	got := ResolveTR143Capability(map[string]bool{"InternetGatewayDevice.DownloadDiagnostics.": true})
	if got.DownloadSupported() || got.UploadSupported() {
		t.Fatalf("legacy-only discovery unexpectedly enabled TR-143: %+v", got)
	}
}
