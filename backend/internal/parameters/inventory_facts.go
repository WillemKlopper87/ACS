package parameters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"acs/internal/store"
)

// InventoryFacts is the deliberately small, allow-listed subset of cached CPE
// parameters that is safe and useful to expose through resource inventory.
// The raw parameter cache is never returned by this API.
type InventoryFacts struct {
	SoftwareVersion string
	HardwareVersion string
}

var softwareVersionPaths = []string{
	"Device.DeviceInfo.SoftwareVersion",
	"InternetGatewayDevice.DeviceInfo.SoftwareVersion",
	"Device.DeviceInfo.AdditionalSoftwareVersion",
	"InternetGatewayDevice.DeviceInfo.AdditionalSoftwareVersion",
}

var hardwareVersionPaths = []string{
	"Device.DeviceInfo.HardwareVersion",
	"InternetGatewayDevice.DeviceInfo.HardwareVersion",
}

// InventoryFactsForDevices extracts only explicitly approved inventory values
// from the cached parameter JSON for a page of devices. Values are evidence of
// the last observation, not live reads; callers must not treat them as current
// reachability state.
func (r *Repository) InventoryFactsForDevices(ctx context.Context, deviceIDs []string) (map[string]InventoryFacts, error) {
	out := make(map[string]InventoryFacts, len(deviceIDs))
	if len(deviceIDs) == 0 {
		return out, nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT device_id::text, parameters
		FROM device_parameter_cache
		WHERE device_id::text = ANY($1)
	`, store.StringArray(deviceIDs))
	if err != nil {
		return nil, fmt.Errorf("query inventory parameter facts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var deviceID string
		var raw []byte
		if err := rows.Scan(&deviceID, &raw); err != nil {
			return nil, fmt.Errorf("scan inventory parameter facts: %w", err)
		}
		cached := map[string]CachedValue{}
		if err := json.Unmarshal(raw, &cached); err != nil {
			return nil, fmt.Errorf("decode inventory parameter facts for %s: %w", deviceID, err)
		}
		out[deviceID] = InventoryFacts{
			SoftwareVersion: firstNonEmptyCached(cached, softwareVersionPaths),
			HardwareVersion: firstNonEmptyCached(cached, hardwareVersionPaths),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inventory parameter facts: %w", err)
	}
	return out, nil
}

func firstNonEmptyCached(cached map[string]CachedValue, paths []string) string {
	for _, path := range paths {
		if value, ok := cached[path]; ok {
			if trimmed := strings.TrimSpace(value.Value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
