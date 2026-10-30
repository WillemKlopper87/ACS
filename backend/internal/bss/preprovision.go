package bss

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Pre-provisioning (docs/ONBOARDING.md): a sale the BSS sends before the
// CPE has ever connected is recorded as a PLANNED onboarding. cmd/acs moves
// the onboarding as the CPE checks in; cmd/bssadapter follows it here,
// writing the account mapping PENDING_ACTIVE on first contact and ACTIVE at
// IN_SERVICE, and completing the TMF641 item that asked for it.

// ItemAwaitingDevice is the TMF641 item status of an add whose CPE has not
// connected yet. Later items in the order wait behind it.
const ItemAwaitingDevice = "AWAITING_DEVICE"

// ParseOUISerial splits a BSS device identity into the OUI and serial
// number the CPE reports in its Inform: "OUI+ProductClass+Serial",
// "OUI-ProductClass-Serial", "OUI-Serial", or a bare serial.
func ParseOUISerial(s string) (oui, serial string) {
	s = strings.TrimSpace(s)
	sep := "+"
	if !strings.Contains(s, sep) {
		sep = "-"
	}
	parts := strings.Split(s, sep)
	if len(parts) < 2 {
		return "", s
	}
	return strings.ToUpper(parts[0]), parts[len(parts)-1]
}

// SetMappingStatus changes a current mapping's status.
func (r *Repository) SetMappingStatus(ctx context.Context, mappingID, status string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE account_device_mappings SET status = $2, updated_at = now()
		WHERE id = $1 AND unassigned_at IS NULL`, mappingID, status)
	if err != nil {
		return fmt.Errorf("set mapping status: %w", err)
	}
	return nil
}

// EndMapping closes one current mapping, keeping it as history.
func (r *Repository) EndMapping(ctx context.Context, mappingID, reason string) error {
	if !ValidUnassignReason(reason) {
		return fmt.Errorf("%w: %s", ErrInvalidUnassignReason, reason)
	}
	_, err := r.db.ExecContext(ctx, `UPDATE account_device_mappings SET unassigned_at = now(), unassign_reason = $2, updated_at = now()
		WHERE id = $1 AND unassigned_at IS NULL`, mappingID, reason)
	if err != nil {
		return fmt.Errorf("end mapping: %w", err)
	}
	return nil
}

// PreProvisioned is a BSS onboarding whose account mapping needs a change.
type PreProvisioned struct {
	OnboardingID    string
	State           string
	DeviceID        string
	DeviceOUISerial string
	AccountID       string
	Role            string
	ServicePlan     string
	MappingID       string
	MappingStatus   string
}

// PreProvisionedNeedingSync lists BSS onboardings whose mapping is behind
// their state: bound to a device but not mapped yet, in service but still
// PENDING_ACTIVE, or cancelled with the mapping still current.
func (r *Repository) PreProvisionedNeedingSync(ctx context.Context, limit int) ([]PreProvisioned, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT o.id, o.state, COALESCE(o.device_id::text, ''), COALESCE(d.oui_serial, ''), o.account_ref,
		       COALESCE(o.bss_role, 'gateway'), COALESCE(o.bss_service_plan, ''), COALESCE(m.id::text, ''), COALESCE(m.status, '')
		  FROM onboardings o
		  LEFT JOIN devices d ON d.id = o.device_id
		  LEFT JOIN account_device_mappings m ON m.id = o.bss_mapping_id
		 WHERE o.source = 'bss' AND o.account_ref IS NOT NULL
		   AND ((o.device_id IS NOT NULL AND o.bss_mapping_id IS NULL AND o.state <> 'CANCELLED')
		     OR (o.state = 'IN_SERVICE' AND m.status = $2 AND m.unassigned_at IS NULL)
		     OR (o.state = 'CANCELLED' AND m.id IS NOT NULL AND m.unassigned_at IS NULL))
		 ORDER BY o.updated_at LIMIT $1`, limit, StatusPendingActive)
	if err != nil {
		return nil, fmt.Errorf("list pre-provisioned onboardings: %w", err)
	}
	defer rows.Close()
	var out []PreProvisioned
	for rows.Next() {
		var p PreProvisioned
		if err := rows.Scan(&p.OnboardingID, &p.State, &p.DeviceID, &p.DeviceOUISerial, &p.AccountID, &p.Role, &p.ServicePlan,
			&p.MappingID, &p.MappingStatus); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetOnboardingMapping records the mapping written for a BSS onboarding.
func (r *Repository) SetOnboardingMapping(ctx context.Context, onboardingID, mappingID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE onboardings SET bss_mapping_id = $2 WHERE id = $1`, onboardingID, mappingID)
	if err != nil {
		return fmt.Errorf("record onboarding mapping: %w", err)
	}
	return nil
}

// MarkItemAwaitingDevice parks a TMF641 add item on its onboarding.
func (r *Repository) MarkItemAwaitingDevice(ctx context.Context, itemID, onboardingID string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE service_order_items SET status = $3, onboarding_id = $2, last_error = NULL, claimed_at = NULL
		WHERE id = $1`, itemID, onboardingID, ItemAwaitingDevice)
	if err != nil {
		return fmt.Errorf("park service order item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AwaitingItem is a TMF641 item waiting for its onboarding.
type AwaitingItem struct {
	ItemID         string
	OrderID        string
	Seq            int
	OnboardingID   string
	State          string
	Reason         string
	MappingID      string
	LastError      string
	OrderCancelled bool
}

func (r *Repository) AwaitingDeviceItems(ctx context.Context, limit int) ([]AwaitingItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT i.id, i.order_id, i.seq, o.id, o.state, COALESCE(o.state_reason, ''), COALESCE(o.bss_mapping_id::text, ''),
		       COALESCE(i.last_error, ''), so.cancelled_at IS NOT NULL
		  FROM service_order_items i
		  JOIN onboardings o ON o.id = i.onboarding_id
		  JOIN service_orders so ON so.id = i.order_id
		 WHERE i.status = $2
		 ORDER BY so.created_at, i.seq LIMIT $1`, limit, ItemAwaitingDevice)
	if err != nil {
		return nil, fmt.Errorf("list items awaiting a device: %w", err)
	}
	defer rows.Close()
	var out []AwaitingItem
	for rows.Next() {
		var a AwaitingItem
		if err := rows.Scan(&a.ItemID, &a.OrderID, &a.Seq, &a.OnboardingID, &a.State, &a.Reason, &a.MappingID, &a.LastError, &a.OrderCancelled); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// NoteAwaitingItem records why a waiting item is not done yet (the
// onboarding's state and reason), without changing its status.
func (r *Repository) NoteAwaitingItem(ctx context.Context, itemID, note string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE service_order_items SET last_error = NULLIF($2, '') WHERE id = $1 AND status = $3`,
		itemID, note, ItemAwaitingDevice)
	return err
}
