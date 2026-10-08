package devices

import (
	"testing"
	"time"
)

func TestConnectionDiagnosticsDoNotInventProbeResults(t *testing.T) {
	now := time.Now()
	status := "TLS_FAILURE"
	nat := true
	d := &Device{CWMPAuthMode: AuthModeDigest, LastConnectionRequestStatus: &status, NATDetected: &nat}
	checks := ConnectionChecks(d, &UspAgent{MTPKind: "MQTT", Connected: false, LastSeenAt: now}, now)
	found := false
	for _, check := range checks {
		if check.Category == "cwmp_authentication" && check.Status != "unknown" {
			t.Fatal("authentication inferred without contact")
		}
		if check.Category == "connection_request" {
			found = check.Evidence == "TLS_FAILURE" && check.Action != ""
		}
	}
	if !found {
		t.Fatal("missing actionable TLS result")
	}
}
