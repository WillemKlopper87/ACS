package cwmp

import "testing"

func TestHasEventCodeMatchesWholeToken(t *testing.T) {
	inform := Inform{Event: []EventStruct{{EventCode: "10 AUTONOMOUS TRANSFER COMPLETE"}, {EventCode: " 8 DIAGNOSTICS COMPLETE"}, {EventCode: "M Reboot"}}}
	if inform.HasEventCode("1") {
		t.Error(`"1" must not match "10 AUTONOMOUS TRANSFER COMPLETE"`)
	}
	if !inform.HasEventCode("10") {
		t.Error(`"10" should match "10 AUTONOMOUS TRANSFER COMPLETE"`)
	}
	if !inform.HasEventCode(EventDiagnosticsComplete) {
		t.Error("8 DIAGNOSTICS COMPLETE not detected")
	}
	if !inform.HasEventCode("M") {
		t.Error(`"M" should match "M Reboot"`)
	}
	if (Inform{Event: []EventStruct{{EventCode: "2 PERIODIC"}}}).HasEventCode(EventDiagnosticsComplete) {
		t.Error("periodic inform reported as diagnostics complete")
	}
}
