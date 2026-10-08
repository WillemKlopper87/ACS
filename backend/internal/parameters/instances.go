package parameters

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RequireInstances rejects fabricated numeric rows when only templates were
// discovered. Evidence expires after a day even without a firmware change.
func RequireInstances(path, firmware string, evidence []InstanceEvidence, now time.Time) error {
	parts := strings.Split(path, ".")
	for i, part := range parts {
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			continue
		}
		instance := strings.Join(parts[:i+1], ".") + "."
		found := false
		for _, snapshot := range evidence {
			if snapshot.Firmware != firmware || now.Sub(snapshot.DiscoveredAt) > 24*time.Hour || snapshot.DiscoveredAt.After(now.Add(time.Minute)) {
				continue
			}
			for _, known := range snapshot.Instances {
				if known == instance {
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("instance %s has no current evidence; run instance discovery", instance)
		}
	}
	return nil
}
