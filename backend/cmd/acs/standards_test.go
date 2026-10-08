package main

import (
	"acs/internal/jobs"
	"encoding/json"
	"strings"
	"testing"
)

func TestTR143RequestsUseStandardURLLeaves(t *testing.T) {
	h := &handler{}
	for _, direction := range []string{"download", "upload"} {
		kind, prefix, urlLeaf := jobs.TypeDiagnosticsDownload, "Device.IP.Diagnostics.DownloadDiagnostics.", "DownloadURL"
		if direction == "upload" {
			kind, prefix, urlLeaf = jobs.TypeDiagnosticsUpload, "Device.IP.Diagnostics.UploadDiagnostics.", "UploadURL"
		}
		raw, _ := json.Marshal(map[string]any{"url": "https://test.example/file", "prefix": prefix, "test_file_length": uint64(4096)})
		request, ok := h.renderJobRequest(&jobs.Job{Type: kind, Payload: raw, Attempts: 1, CommandKey: "test"})
		if !ok || !strings.Contains(string(request), "<Name>"+prefix+urlLeaf+"</Name>") || strings.Contains(string(request), "<Name>"+prefix+"URL</Name>") {
			t.Fatalf("bad TR-143 %s request: %s", direction, request)
		}
		if direction == "upload" && !strings.Contains(string(request), `xsi:type="xsd:unsignedLong">4096`) {
			t.Fatal("upload size lost its scalar type")
		}
	}
}
func TestIncrementalDiscoveryUsesNextLevel(t *testing.T) {
	raw, _ := json.Marshal(jobs.ParameterDiscoveryPayload{Root: "Device.", Cursor: "Device.WiFi.", Incremental: true})
	body, ok := (&handler{}).renderJobRequest(&jobs.Job{Type: jobs.TypeParameterDiscovery, Payload: raw})
	if !ok || !strings.Contains(string(body), "<ParameterPath>Device.WiFi.</ParameterPath>") || !strings.Contains(string(body), "<NextLevel>1</NextLevel>") {
		t.Fatal(string(body))
	}
}
