package bss

import (
	"context"
	"fmt"

	"acs/internal/store"
)

// ActiveRolesForDevices returns the distinct current account-assignment roles
// for each requested device. A device can legitimately serve different roles
// for different accounts, so the result is a slice rather than a guessed
// single global role. Historical/unassigned rows are deliberately excluded.
func (r *Repository) ActiveRolesForDevices(ctx context.Context, deviceIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(deviceIDs))
	if len(deviceIDs) == 0 {
		return out, nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT device_id::text, role
		FROM account_device_mappings
		WHERE device_id::text = ANY($1)
		  AND unassigned_at IS NULL
		ORDER BY device_id::text, role
	`, store.StringArray(deviceIDs))
	if err != nil {
		return nil, fmt.Errorf("query active device assignment roles: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]map[string]struct{})
	for rows.Next() {
		var deviceID, role string
		if err := rows.Scan(&deviceID, &role); err != nil {
			return nil, fmt.Errorf("scan active device assignment role: %w", err)
		}
		if _, ok := seen[deviceID]; !ok {
			seen[deviceID] = map[string]struct{}{}
		}
		if _, duplicate := seen[deviceID][role]; duplicate {
			continue
		}
		seen[deviceID][role] = struct{}{}
		out[deviceID] = append(out[deviceID], role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active device assignment roles: %w", err)
	}
	return out, nil
}
