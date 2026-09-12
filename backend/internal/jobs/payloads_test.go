package jobs_test

import (
	"encoding/json"
	"testing"

	"acs/internal/jobs"
)

// TestAddObjectPayloadJSONRoundTrip covers AddObjectPayload's new
// Parameters field: existing CWMP-created payloads (no Parameters) must
// round-trip unchanged thanks to omitempty, and a payload that does set
// initial values on the object about to be created must round-trip those
// too.
func TestAddObjectPayloadJSONRoundTrip(t *testing.T) {
	t.Run("without Parameters", func(t *testing.T) {
		want := jobs.AddObjectPayload{ObjectPath: "Device.WiFi.SSID."}

		data, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"object_path":"Device.WiFi.SSID."}` {
			t.Errorf("marshaled = %s, want no \"parameters\" key (omitempty)", data)
		}

		var got jobs.AddObjectPayload
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.ObjectPath != want.ObjectPath || len(got.Parameters) != 0 {
			t.Errorf("round-tripped = %+v, want %+v", got, want)
		}
	})

	t.Run("with Parameters", func(t *testing.T) {
		want := jobs.AddObjectPayload{
			ObjectPath: "Device.WiFi.SSID.",
			Parameters: []jobs.ParameterWrite{
				{Name: "SSID", Value: "MyNetwork", Type: "xsd:string"},
				{Name: "Enable", Value: "true", Type: "xsd:boolean"},
			},
		}

		data, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}

		var got jobs.AddObjectPayload
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.ObjectPath != want.ObjectPath || len(got.Parameters) != len(want.Parameters) {
			t.Fatalf("round-tripped = %+v, want %+v", got, want)
		}
		for i := range want.Parameters {
			if got.Parameters[i] != want.Parameters[i] {
				t.Errorf("Parameters[%d] = %+v, want %+v", i, got.Parameters[i], want.Parameters[i])
			}
		}
	})
}
