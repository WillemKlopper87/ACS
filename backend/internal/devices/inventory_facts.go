package devices

import (
	"context"
	"encoding/json"
	"fmt"

	"acs/internal/store"
)

// ManagementProtocolsFor returns the management transports the ACS has
// positively observed for each requested device. It is intentionally a batch
// lookup so northbound inventory pages do not turn into an N+1 query pattern.
// The devices.management_protocols CHECK constraint limits values to CWMP/USP.
func (r *Repository) ManagementProtocolsFor(ctx context.Context, deviceIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(deviceIDs))
	if len(deviceIDs) == 0 {
		return out, nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, to_json(management_protocols)::text
		FROM devices
		WHERE id::text = ANY($1)
	`, store.StringArray(deviceIDs))
	if err != nil {
		return nil, fmt.Errorf("query device management protocols: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("scan device management protocols: %w", err)
		}
		var protocols []string
		if err := json.Unmarshal([]byte(raw), &protocols); err != nil {
			return nil, fmt.Errorf("decode device management protocols for %s: %w", id, err)
		}
		out[id] = protocols
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate device management protocols: %w", err)
	}
	return out, nil
}
