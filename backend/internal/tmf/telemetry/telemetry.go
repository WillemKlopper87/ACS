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
}

type Fault struct {
	AccountID, DeviceID, JobID, Protocol, Code, Message string
	At                                                  time.Time
}

func SourceKey(protocol, deviceID, identity string) string {
	h := sha256.Sum256([]byte(protocol + "|" + deviceID + "|" + identity))
	return "acs-fault-" + hex.EncodeToString(h[:])
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
	alarmpayload, _ := json.Marshal(map[string]any{"eventSourceKey": key, "protocol": f.Protocol, "jobId": f.JobID})
	_, err := sink.CreateAlarm(ctx, key, f.AccountID, f.DeviceID, "", "DeviceFault", severity(f.Code), f.Code, f.Message, alarmpayload)
	return err
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
