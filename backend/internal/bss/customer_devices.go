package bss

import (
	"context"
	"fmt"
	"time"

	"acs/internal/store"
)

// CustomerDevice is one of a customer's devices as /bss/v1/devices shows
// it: identity, its current account assignment (if any) and reachability.
type CustomerDevice struct {
	ID            string     `json:"id"`
	OUISerial     string     `json:"oui_serial"`
	AccountID     *string    `json:"account_id,omitempty"`
	Role          *string    `json:"role,omitempty"`
	MappingStatus *string    `json:"mapping_status,omitempty"`
	OnlineStatus  string     `json:"online_status"`
	LastInformAt  *time.Time `json:"last_inform_at,omitempty"`
}

// CustomerDevices pages through a customer's devices in oui_serial order,
// after the given oui_serial (empty = from the start). accounts, when set,
// limits the page to devices currently mapped to one of them.
func (r *Repository) CustomerDevices(ctx context.Context, customerID, after string, limit int, accounts []string) ([]CustomerDevice, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT d.id, d.oui_serial, m.account_id, m.role, m.status, d.online_status, d.last_inform_at
		  FROM devices d
		  LEFT JOIN account_device_mappings m ON m.device_id = d.id AND m.unassigned_at IS NULL
		 WHERE d.customer_id = $1::uuid AND d.oui_serial > $2
		   AND ($4::text[] IS NULL OR m.account_id = ANY($4::text[]))
		 ORDER BY d.oui_serial LIMIT $3`, customerID, after, limit, store.StringArray(accounts))
	if err != nil {
		return nil, fmt.Errorf("list customer devices: %w", err)
	}
	defer rows.Close()
	out := []CustomerDevice{}
	for rows.Next() {
		var d CustomerDevice
		if err := rows.Scan(&d.ID, &d.OUISerial, &d.AccountID, &d.Role, &d.MappingStatus, &d.OnlineStatus, &d.LastInformAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
