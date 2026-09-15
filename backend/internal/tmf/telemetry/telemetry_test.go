package telemetry

import (
	"acs/internal/bss"
	"context"
	"encoding/json"
	"testing"
	"time"
)

type fakeSink struct {
	events, alarms int
	keys           []string
	cleared        []string
}

func (f *fakeSink) CreateEvent(_ context.Context, key, _, _, _, _ string, _ json.RawMessage, _ time.Time) (*bss.EventRecord, error) {
	f.events++
	f.keys = append(f.keys, key)
	return nil, nil
}
func (f *fakeSink) CreateAlarm(_ context.Context, key, _, _, _, _, _, _, _ string, _ json.RawMessage) (*bss.AlarmRecord, error) {
	f.alarms++
	f.keys = append(f.keys, key)
	return nil, nil
}
func (f *fakeSink) ClearAlarm(_ context.Context, account, device, key string) error {
	f.cleared = append(f.cleared, account+"|"+device+"|"+key)
	return nil
}

func TestPublishFaultStableKeyAndLifecycleMapping(t *testing.T) {
	f := &fakeSink{}
	at := time.Unix(10, 0)
	in := Fault{AccountID: "acct-1", DeviceID: "dev-1", JobID: "job-1", Protocol: "CWMP", Code: "9002", Message: "timeout", At: at}
	if err := PublishFault(context.Background(), f, in); err != nil {
		t.Fatal(err)
	}
	if SourceKey("CWMP", "dev-1", "job-1|9002|timeout") != f.keys[0] || f.events != 1 || f.alarms != 1 {
		t.Fatalf("unexpected publication: %#v", f)
	}
	if severity("timeout") != "major" || severity("9002") != "minor" {
		t.Fatal("lifecycle severity mapping failed")
	}
}

func TestPublishRecoveryUsesStableConditionAndTenantScope(t *testing.T) {
	f := &fakeSink{}
	if err := PublishRecovery(context.Background(), f, "acct-1", "dev-1", "CWMP", "9002"); err != nil {
		t.Fatal(err)
	}
	want := "acct-1|dev-1|" + ConditionKey("CWMP", "dev-1", "9002")
	if len(f.cleared) != 1 || f.cleared[0] != want {
		t.Fatalf("cleared = %#v, want %#v", f.cleared, want)
	}
}

func TestConditionKeyStableAcrossJobRetries(t *testing.T) {
	if ConditionKey("CWMP", "dev", "9002") != ConditionKey("CWMP", "dev", "9002") {
		t.Fatal("condition key changed across retries")
	}
	if ConditionKey("CWMP", "dev", "9002") == ConditionKey("CWMP", "dev", "9005") {
		t.Fatal("different fault conditions collided")
	}
}

func TestQualifyingRecoveryEventRequiresPositiveSignal(t *testing.T) {
	for _, name := range []string{"DeviceRecovered", "CPE online", "fault resolved", "healthy"} {
		if !QualifyingRecoveryEvent(name) {
			t.Errorf("%q was not recognized as recovery", name)
		}
	}
	for _, name := range []string{"DeviceFault", "offline", "timeout"} {
		if QualifyingRecoveryEvent(name) {
			t.Errorf("%q was incorrectly recognized as recovery", name)
		}
	}
}

func TestPublishFaultDeduplicationKeyIsStableAcrossRetries(t *testing.T) {
	a := SourceKey("USP", "dev", "job|7|bad")
	b := SourceKey("USP", "dev", "job|7|bad")
	if a != b {
		t.Fatal("deduplication key changed across retry")
	}
}
