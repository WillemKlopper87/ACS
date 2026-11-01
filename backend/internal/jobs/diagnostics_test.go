package jobs

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDiagnosticsTriggeredDependsOnDeadlineNotAttempts(t *testing.T) {
	deadline := time.Now()
	if DiagnosticsTriggered(&Job{Type: TypeDiagnosticsPing, Attempts: 5}) {
		t.Error("a requeued trigger (attempts 5, no deadline) must be re-triggered, not polled")
	}
	if !DiagnosticsTriggered(&Job{Type: TypeDiagnosticsPing, Attempts: 1, DiagnosticsDeadline: &deadline}) {
		t.Error("an acknowledged trigger must be polled")
	}
}

func TestDiagnosticsWindowCoversExpectedRunTime(t *testing.T) {
	ping, _ := json.Marshal(DiagnosticsPingPayload{Host: "8.8.8.8", NumberOfRepetitions: 4, Timeout: 5000})
	if got, want := DiagnosticsWindow(&Job{Type: TypeDiagnosticsPing, Payload: ping}), diagnosticsBaseWindow+20*time.Second; got != want {
		t.Errorf("ping window = %v, want %v", got, want)
	}
	trace, _ := json.Marshal(DiagnosticsTraceroutePayload{Host: "8.8.8.8", NumberOfTries: 3, Timeout: 5000, MaxHopCount: 30})
	if got, want := DiagnosticsWindow(&Job{Type: TypeDiagnosticsTraceroute, Payload: trace}), diagnosticsBaseWindow+450*time.Second; got != want {
		t.Errorf("traceroute window = %v, want %v", got, want)
	}
	if got := DiagnosticsWindow(&Job{Type: TypeDiagnosticsDownload, Payload: json.RawMessage(`{}`)}); got != diagnosticsBaseWindow {
		t.Errorf("TR-143 window = %v, want base %v", got, diagnosticsBaseWindow)
	}
	huge, _ := json.Marshal(DiagnosticsPingPayload{NumberOfRepetitions: 1 << 20, Timeout: 1 << 20})
	if got := DiagnosticsWindow(&Job{Type: TypeDiagnosticsPing, Payload: huge}); got != diagnosticsMaxWindow {
		t.Errorf("huge window = %v, want capped %v", got, diagnosticsMaxWindow)
	}
}

func TestDiagnosticsExpired(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Second), now.Add(time.Second)
	if DiagnosticsExpired(&Job{}, now) {
		t.Error("untriggered job cannot be expired")
	}
	if DiagnosticsExpired(&Job{DiagnosticsDeadline: &future}, now) {
		t.Error("future deadline reported expired")
	}
	if !DiagnosticsExpired(&Job{DiagnosticsDeadline: &past}, now) {
		t.Error("past deadline not reported expired")
	}
}

func TestIsCWMPDiagnosticType(t *testing.T) {
	for _, typ := range []string{TypeDiagnosticsPing, TypeDiagnosticsTraceroute, TypeDiagnosticsDownload, TypeDiagnosticsUpload} {
		if !IsCWMPDiagnosticType(typ) {
			t.Errorf("%s should be a diagnostic type", typ)
		}
	}
	if IsCWMPDiagnosticType(TypeGetParameter) {
		t.Error("GET_PARAMETER is not a diagnostic")
	}
}
