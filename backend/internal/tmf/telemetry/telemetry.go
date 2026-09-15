package telemetry

import (
	"acs/internal/bss"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Sink is implemented by the existing TMF event/alarm repository. Keeping
// this boundary small makes the mapping deterministic and independently testable.
type Sink interface {
	CreateEvent(context.Context, string, string, string, string, string, json.RawMessage, time.Time) (*bss.EventRecord, error)
	CreateAlarm(context.Context, string, string, string, string, string, string, string, string, json.RawMessage) (*bss.AlarmRecord, error)
	ClearAlarm(context.Context, string, string, string) error
}

type Fault struct {
	AccountID, DeviceID, JobID, Protocol, Code, Message string
	At                                                  time.Time
}

func SourceKey(protocol, deviceID, identity string) string {
	h := sha256.Sum256([]byte(protocol + "|" + deviceID + "|" + identity))
	return "acs-fault-" + hex.EncodeToString(h[:])
}

// ConditionKey identifies the underlying operational condition. Unlike an
// event key it deliberately excludes job/message details, so retries and a
// later recovery signal address the same durable alarm while events remain
// independently recorded in the history table.
func ConditionKey(protocol, deviceID, code string) string {
	h := sha256.Sum256([]byte(protocol + "|" + deviceID + "|" + code))
	return "acs-condition-" + hex.EncodeToString(h[:])
}

// PublishFault writes the event and alarm with the same stable source key.
// Repository uniqueness makes retries after crashes harmless.
func PublishFault(ctx context.Context, sink Sink, f Fault) error {
	if sink == nil || strings.TrimSpace(f.DeviceID) == "" || strings.TrimSpace(f.Message) == "" {
		return fmt.Errorf("invalid fault or nil TMF sink")
	}
	if f.At.IsZero() {
		f.At = time.Now().UTC()
	}
	key := SourceKey(f.Protocol, f.DeviceID, f.JobID+"|"+f.Code+"|"+f.Message)
	payload, _ := json.Marshal(map[string]any{"protocol": f.Protocol, "jobId": f.JobID, "faultCode": f.Code, "message": f.Message})
	if _, err := sink.CreateEvent(ctx, key, f.AccountID, f.DeviceID, "", "DeviceFault", payload, f.At); err != nil {
		return err
	}
	condition := ConditionKey(f.Protocol, f.DeviceID, f.Code)
	alarmpayload, _ := json.Marshal(map[string]any{"eventSourceKey": key, "conditionKey": condition, "protocol": f.Protocol, "jobId": f.JobID})
	_, err := sink.CreateAlarm(ctx, condition, f.AccountID, f.DeviceID, "", "DeviceFault", severity(f.Code), f.Code, f.Message, alarmpayload)
	return err
}

// PublishRecovery clears the active alarm for a previously observed stable
// condition. The event stream remains append-only; only the alarm lifecycle
// changes. The repository enforces account scoping and only clears raised
// alarms, making duplicate or late recovery signals harmless.
func PublishRecovery(ctx context.Context, sink Sink, accountID, deviceID, protocol, code string) error {
	if sink == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(code) == "" {
		return fmt.Errorf("invalid recovery or nil TMF sink")
	}
	at := time.Now().UTC()
	key := SourceKey(protocol, deviceID, "recovery|"+code+"|"+at.Format(time.RFC3339Nano))
	payload, _ := json.Marshal(map[string]any{"protocol": protocol, "faultCode": code, "conditionKey": ConditionKey(protocol, deviceID, code), "recoveredAt": at})
	if _, err := sink.CreateEvent(ctx, key, accountID, deviceID, "", "DeviceRecovered", payload, at); err != nil {
		return err
	}
	return sink.ClearAlarm(ctx, accountID, deviceID, ConditionKey(protocol, deviceID, code))
}

func severity(code string) string {
	if strings.Contains(strings.ToLower(code), "timeout") {
		return "major"
	}
	return "minor"
}

// QualifyingUSPEvent identifies notifications that represent an operational fault.
func QualifyingUSPEvent(name string) bool {
	n := strings.ToLower(name)
	for _, term := range []string{"fault", "error", "fail", "offline", "timeout", "alarm"} {
		if strings.Contains(n, term) {
			return true
		}
	}
	return false
}

// QualifyingRecoveryEvent recognizes the positive signals that may close a
// previously raised device condition. Callers still provide the fault code
// used to derive the condition key; a generic online event cannot clear an
// unrelated fault.
func QualifyingRecoveryEvent(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, term := range []string{"recovered", "restored", "online", "resolved", "success", "healthy"} {
		if strings.Contains(n, term) {
			return true
		}
	}
	return false
}
