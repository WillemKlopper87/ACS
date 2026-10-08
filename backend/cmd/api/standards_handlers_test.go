package main

import (
	"acs/internal/datamodel"
	"acs/internal/jobs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStandardsAssessmentRecognizesInstancesAndExtensions(t *testing.T) {
	d, err := datamodel.Load("cwmp", "2.20")
	if err != nil {
		t.Fatal(err)
	}
	out := assessPaths(d, map[string]bool{"Device.WiFi.SSID.2.Enable": true, "Device.X_HUAWEI.RSRP": false, "Device.Future": false})
	counts := map[string]int{}
	for _, a := range out {
		counts[a.Classification]++
	}
	if counts["standard"] != 1 || counts["vendor_extension"] != 1 || counts["unrecognized_in_selected_dictionary"] != 1 {
		t.Fatal(out)
	}
}
func TestWriteGuardAcceptsUSPTemplatesWithoutGrantingReadOnlyAccess(t *testing.T) {
	payload := jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.WiFi.SSID.2.Enable"}}}
	if err := validateDiscoveredWritableParameters(map[string]bool{"Device.WiFi.SSID.{i}.Enable": true}, payload); err != nil {
		t.Fatal(err)
	}
	if err := validateDiscoveredWritableParameters(map[string]bool{"Device.WiFi.SSID.{i}.Enable": false}, payload); err == nil {
		t.Fatal("read-only template allowed write")
	}
}
func TestBulkReceiverRejectsAnonymousRequestsBeforeStorage(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bulk-data/11111111-1111-1111-1111-111111111111/receive", strings.NewReader(`{"Report":[]}`))
	req.SetPathValue("id", "11111111-1111-1111-1111-111111111111")
	w := httptest.NewRecorder()
	(&handler{}).receiveBulkData(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
}
