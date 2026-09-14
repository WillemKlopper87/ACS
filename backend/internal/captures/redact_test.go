package captures

import "testing"

func TestRedactParamValue(t *testing.T) {
	sensitive := []string{
		"Device.WiFi.AccessPoint.1.Security.KeyPassphrase",
		"Device.WiFi.AccessPoint.1.Security.PreSharedKey.1.KeyPassphrase",
		"X_HUAWEI_AdminPassword",
		"Device.ManagementServer.ConnectionRequestPassword",
		"some.PSK.value",
		"Secret",
	}
	for _, name := range sensitive {
		if got := RedactParamValue(name, "letmein123"); got != "***REDACTED***" {
			t.Errorf("RedactParamValue(%q, ...) = %q, want ***REDACTED***", name, got)
		}
	}

	notSensitive := []string{"Device.WiFi.SSID.1.SSID", "Device.DeviceInfo.SoftwareVersion", "Device.WiFi.AccessPoint.1.Enable"}
	for _, name := range notSensitive {
		if got := RedactParamValue(name, "MyNetwork"); got != "MyNetwork" {
			t.Errorf("RedactParamValue(%q, ...) = %q, want the original value unredacted", name, got)
		}
	}
}

func TestRedactAuthHeader(t *testing.T) {
	digest := `Digest username="dev1", realm="acs", nonce="abc", uri="/cwmp", response="deadbeef"`
	if got := RedactAuthHeader(digest); got != digest {
		t.Errorf("RedactAuthHeader(Digest) = %q, want it captured verbatim (response is a one-way hash)", got)
	}

	basic := "Basic ZGV2MTpzM2NyZXQ="
	got := RedactAuthHeader(basic)
	if got == basic {
		t.Error("RedactAuthHeader(Basic) returned the header verbatim, want it redacted")
	}
	if got != "Basic auth, username=dev1" {
		t.Errorf("RedactAuthHeader(Basic) = %q, want %q", got, "Basic auth, username=dev1")
	}

	if got := RedactAuthHeader(""); got != "" {
		t.Errorf("RedactAuthHeader(\"\") = %q, want empty", got)
	}
}
