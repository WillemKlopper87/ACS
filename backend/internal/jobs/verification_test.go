package jobs

import (
	"encoding/json"
	"testing"
)

func TestReadbackDistinguishesMismatchMissingAndSecrets(t *testing.T) {
	p := SetParameterPayload{Parameters: []ParameterWrite{{Name: "Device.WiFi.SSID.1.Enable", Value: "true", Type: "xsd:boolean"}, {Name: "Device.WiFi.SSID.1.SSID", Value: "wanted"}, {Name: "Device.X_Test.Missing", Value: "value"}, {Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: "never-export-this"}}}
	check := CompareReadback(p, map[string]string{"Device.WiFi.SSID.1.Enable": "1", "Device.WiFi.SSID.1.SSID": "different"})
	if len(check.Matched) != 1 || len(check.Mismatched) != 1 || len(check.Missing) != 1 || len(check.Skipped) != 1 {
		t.Fatalf("incorrect evidence: %+v", check)
	}
	raw, _ := json.Marshal(check)
	if string(raw) == "" || SensitivePath("Device.WiFi.SSID.1.SSID") {
		t.Fatal("incorrect sensitivity classification")
	}
	if len(VerificationPaths(p)) != 3 {
		t.Fatal("secret readback was queued")
	}
}
