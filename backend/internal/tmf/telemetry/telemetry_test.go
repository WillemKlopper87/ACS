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

func TestPublishFaultDeduplicationKeyIsStableAcrossRetries(t *testing.T) {
	a := SourceKey("USP", "dev", "job|7|bad")
	b := SourceKey("USP", "dev", "job|7|bad")
	if a != b {
		t.Fatal("deduplication key changed across retry")
	}
}
