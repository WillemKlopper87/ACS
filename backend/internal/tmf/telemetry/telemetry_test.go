package telemetry

import (
	"acs/internal/bss"
	"context"
	"encoding/json"
	"strings"
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

func TestPublishCellularStateChangedRequiresChanges(t *testing.T) {
	f := &fakeSink{}
	err := PublishCellularStateChanged(context.Background(), f, "acct-1", "dev-1", "CWMP", nil, time.Unix(1, 0))
	if err == nil {
		t.Fatal("expected error for empty change set")
	}
	if f.events != 0 {
		t.Fatalf("no event should be published for an empty change set, got %d", f.events)
	}
}

func TestPublishCellularStateChangedEmitsEvent(t *testing.T) {
	f := &fakeSink{}
	changes := map[string]CellularStateChange{
		"Device.Cellular.Interface.1.CurrentAccessTechnology": {Old: "LTE", New: "NR"},
	}
	if err := PublishCellularStateChanged(context.Background(), f, "acct-1", "dev-1", "CWMP", changes, time.Unix(5, 0)); err != nil {
		t.Fatal(err)
	}
	if f.events != 1 || f.alarms != 0 {
		t.Fatalf("expected exactly one event and no alarm, got %#v", f)
	}
}

// TestCellularStateChangeJSONShapeIsLowerCamel locks in the wire shape
// documented in bss-integration-guide.md §4.3: a webhook subscriber decodes
// this payload with an ordinary JSON library, so "Old"/"New" (Go's default
// field-name marshaling, no tags) would silently break every consumer that
// followed the docs.
func TestCellularStateChangeJSONShapeIsLowerCamel(t *testing.T) {
	body, err := json.Marshal(CellularStateChange{Old: "LTE", New: "NR"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), `{"old":"LTE","new":"NR"}`; got != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
}

func TestPublishFaultDeduplicationKeyIsStableAcrossRetries(t *testing.T) {
	a := SourceKey("USP", "dev", "job|7|bad")
	b := SourceKey("USP", "dev", "job|7|bad")
	if a != b {
		t.Fatal("deduplication key changed across retry")
	}
}

// captureSink keeps what an event carried, which fakeSink drops.
type captureSink struct {
	fakeSink
	eventType string
	payload   string
	device    string
}

func (c *captureSink) CreateEvent(ctx context.Context, key, account, device, _, eventType string, payload json.RawMessage, at time.Time) (*bss.EventRecord, error) {
	c.eventType, c.payload, c.device = eventType, string(payload), device
	return c.fakeSink.CreateEvent(ctx, key, account, device, "", eventType, payload, at)
}

func TestPublishSimChangedCarriesOnlyMaskedHints(t *testing.T) {
	c := &captureSink{}
	change := SimChange{Verdict: "notice", Network: "65501", ICCIDHint: "…0022", IMSIHint: "65501…0022", PreviousICCIDHint: "…0011", PreviousIMSIHint: "65501…0011"}
	if err := PublishSimChanged(context.Background(), c, "acct-1", "dev-1", "CWMP", change, time.Unix(5, 0)); err != nil {
		t.Fatal(err)
	}
	if c.eventType != "SimChanged" || c.device != "dev-1" || c.events != 1 {
		t.Fatalf("event = %q device=%q count=%d", c.eventType, c.device, c.events)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(c.payload), &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"protocol": "CWMP", "verdict": "notice", "network": "65501", "iccidHint": "…0022", "imsiHint": "65501…0022", "previousIccidHint": "…0011", "previousImsiHint": "65501…0011"} {
		if got[k] != want {
			t.Fatalf("payload[%s] = %v, want %q (%s)", k, got[k], want, c.payload)
		}
	}
	if strings.Contains(c.payload, "89270000") {
		t.Fatal("payload must not carry an identifier")
	}
}

func TestPublishSimChangedRequiresADeviceAndAVerdict(t *testing.T) {
	c := &captureSink{}
	if err := PublishSimChanged(context.Background(), c, "a", "", "CWMP", SimChange{Verdict: "notice"}, time.Unix(1, 0)); err == nil {
		t.Fatal("expected an error without a device")
	}
	if err := PublishSimChanged(context.Background(), c, "a", "dev-1", "CWMP", SimChange{}, time.Unix(1, 0)); err == nil {
		t.Fatal("expected an error without a verdict")
	}
	if c.events != 0 {
		t.Fatalf("events = %d, want 0", c.events)
	}
}
