package parameters

import (
	"testing"
	"time"
)

func TestInstanceEvidenceRejectsStaleFirmwareAndUnknownRows(t *testing.T) {
	now := time.Now()
	evidence := []InstanceEvidence{{Firmware: "1.0", DiscoveredAt: now, Instances: []string{"Device.BulkData.Profile.2.", "Device.BulkData.Profile.2.Parameter.7."}}}
	path := "Device.BulkData.Profile.2.Parameter.7.Reference"
	if err := RequireInstances(path, "1.0", evidence, now); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, firmware string
		now            time.Time
	}{{path, "2.0", now}, {path, "1.0", now.Add(25 * time.Hour)}, {"Device.BulkData.Profile.2.Parameter.8.Reference", "1.0", now}} {
		if RequireInstances(test.path, test.firmware, evidence, test.now) == nil {
			t.Fatal("unproven row accepted", test)
		}
	}
}
