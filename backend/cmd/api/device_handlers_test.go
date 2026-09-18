package main

import (
	"strings"
	"testing"

	"acs/internal/jobs"
)

func TestValidateDiscoveredWritableParameters(t *testing.T) {
	payload := jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.WiFi.SSID.1.SSID"}}}

	if err := validateDiscoveredWritableParameters(map[string]bool{"Device.WiFi.SSID.1.SSID": true}, payload); err != nil {
		t.Fatalf("writable parameter rejected: %v", err)
	}
	if err := validateDiscoveredWritableParameters(map[string]bool{"Device.WiFi.SSID.1.SSID": false}, payload); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only parameter error = %v, want read-only rejection", err)
	}
	if err := validateDiscoveredWritableParameters(map[string]bool{}, payload); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("missing parameter error = %v, want unsupported rejection", err)
	}
}
