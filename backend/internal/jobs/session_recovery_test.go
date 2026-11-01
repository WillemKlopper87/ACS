package jobs

import (
	"testing"
	"time"
)

func TestClassifyDroppedSessionJob(t *testing.T) {
	rebootKeys := map[string]map[string]bool{"Reboot": {"reboot_20261030_18ac76da": true}}
	for _, tc := range []struct {
		name string
		job  Job
		keys map[string]map[string]bool
		want DroppedSessionAction
	}{
		{"read is resent", Job{Type: TypeGetParameter, Attempts: 1, MaxAttempts: 3}, nil, DroppedRequeue},
		{"write is resent", Job{Type: TypeSetParameter, Attempts: 2, MaxAttempts: 3}, nil, DroppedRequeue},
		{"exhausted read fails", Job{Type: TypeGetParameter, Attempts: 3, MaxAttempts: 3}, nil, DroppedFailed},
		{"reboot proven by M Reboot", Job{Type: TypeReboot, CommandKey: "reboot_20261030_18ac76da", Attempts: 1, MaxAttempts: 1}, rebootKeys, DroppedSucceeded},
		{"reboot without proof", Job{Type: TypeReboot, CommandKey: "reboot_other", Attempts: 1, MaxAttempts: 1}, rebootKeys, DroppedFailed},
		{"firmware waits for TransferComplete", Job{Type: TypeFirmwareDownload, Attempts: 1, MaxAttempts: 1}, nil, DroppedAwaitTransfer},
		{"upload waits for TransferComplete", Job{Type: TypeUpload, Attempts: 1, MaxAttempts: 1}, nil, DroppedAwaitTransfer},
		{"add object never repeated", Job{Type: TypeAddObject, Attempts: 1, MaxAttempts: 3}, nil, DroppedFailed},
		{"factory reset never repeated", Job{Type: TypeFactoryReset, Attempts: 1, MaxAttempts: 3}, nil, DroppedFailed},
	} {
		if got := ClassifyDroppedSessionJob(&tc.job, tc.keys, false); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestClassifyDroppedSessionDiagnostics covers the FastMile pattern: the
// session carrying a diagnostic trigger drops before the SPV response.
func TestClassifyDroppedSessionDiagnostics(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	for _, tc := range []struct {
		name     string
		job      Job
		complete bool
		want     DroppedSessionAction
	}{
		{"unacked trigger is re-sent", Job{Type: TypeDiagnosticsPing, Attempts: 1, MaxAttempts: DiagnosticsMaxAttempts}, false, DroppedRequeue},
		{"unacked trigger proven by 8 DIAGNOSTICS COMPLETE is polled", Job{Type: TypeDiagnosticsPing, Attempts: 1, MaxAttempts: DiagnosticsMaxAttempts}, true, DroppedDiagnosticsTriggered},
		{"traceroute trigger proven too", Job{Type: TypeDiagnosticsTraceroute, Attempts: 1, MaxAttempts: DiagnosticsMaxAttempts}, true, DroppedDiagnosticsTriggered},
		{"TR-143 trigger proven too", Job{Type: TypeDiagnosticsDownload, Attempts: 1, MaxAttempts: DiagnosticsMaxAttempts}, true, DroppedDiagnosticsTriggered},
		{"dropped poll is polled again", Job{Type: TypeDiagnosticsPing, Attempts: 3, MaxAttempts: DiagnosticsMaxAttempts, DiagnosticsDeadline: &deadline}, true, DroppedRequeue},
		{"8 does not affect other types", Job{Type: TypeGetParameter, Attempts: 1, MaxAttempts: 3}, true, DroppedRequeue},
	} {
		if got := ClassifyDroppedSessionJob(&tc.job, nil, tc.complete); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestLateTransferReconcilable(t *testing.T) {
	code := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		job  Job
		want bool
	}{
		{"lease lost on firmware", Job{Type: TypeFirmwareDownload, Status: StatusFailed, FaultCode: code("LEASE_EXPIRED_UNSAFE_RETRY")}, true},
		{"session dropped on upload", Job{Type: TypeUpload, Status: StatusFailed, FaultCode: code(FaultSessionDropped)}, true},
		{"deadline passed", Job{Type: TypeFirmwareDownload, Status: StatusFailed, FaultCode: code("TRANSFER_TIMEOUT")}, true},
		{"device reported a fault", Job{Type: TypeFirmwareDownload, Status: StatusFailed, FaultCode: code("9010")}, false},
		{"already succeeded", Job{Type: TypeFirmwareDownload, Status: StatusSuccess}, false},
		{"not a transfer", Job{Type: TypeReboot, Status: StatusFailed, FaultCode: code(FaultSessionDropped)}, false},
	} {
		if got := LateTransferReconcilable(&tc.job); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
