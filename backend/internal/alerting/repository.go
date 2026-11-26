package alerting

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Repository struct{ db *sql.DB }

type OfflineDevice struct {
	TenantID, AccountKey, DeviceID, CustomerTier string
	GroupIDs                                     []string
	LastInformAt                                 *time.Time
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, p Policy) (*Policy, error) {
	id := uuid.New().String()
	priorities, err := json.Marshal(p.FaultPriorities)
	if err != nil {
		return nil, fmt.Errorf("marshal fault priorities: %w", err)
	}
	steps, err := json.Marshal(p.Steps)
	if err != nil {
		return nil, fmt.Errorf("marshal escalation steps: %w", err)
	}
	var out Policy
	err = r.db.QueryRowContext(ctx, `INSERT INTO alert_policies (id,name,scope,tenant_id,group_id,device_id,customer_tier,enabled,fault_priorities,offline_after_seconds,steps,account_key) VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,NULLIF($7,''),$8,$9,$10,$11,NULLIF($12,'')::uuid) RETURNING id,name,scope,COALESCE(tenant_id,''),COALESCE(account_key::text,''),COALESCE((SELECT a.customer_id::text FROM bss_accounts a WHERE a.id=alert_policies.account_key),''),COALESCE(group_id::text,''),COALESCE(device_id::text,''),COALESCE(customer_tier,''),enabled,fault_priorities,offline_after_seconds,steps`, id, p.Name, p.Scope, p.TenantID, p.GroupID, p.DeviceID, p.CustomerTier, p.Enabled, priorities, int(p.OfflineAfter/time.Second), steps, p.AccountKey).Scan(&out.ID, &out.Name, &out.Scope, &out.TenantID, &out.AccountKey, &out.CustomerID, &out.GroupID, &out.DeviceID, &out.CustomerTier, &out.Enabled, &priorities, &out.OfflineAfter, &steps)
	if err != nil {
		return nil, fmt.Errorf("create alert policy: %w", err)
	}
	if err := json.Unmarshal(priorities, &out.FaultPriorities); err != nil {
		return nil, err
	}
	out.OfflineAfter *= time.Second
	if err := json.Unmarshal(steps, &out.Steps); err != nil {
		return nil, fmt.Errorf("unmarshal escalation steps: %w", err)
	}
	return &out, nil
}

func (r *Repository) List(ctx context.Context) ([]Policy, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,name,scope,COALESCE(tenant_id,''),COALESCE(account_key::text,''),COALESCE((SELECT a.customer_id::text FROM bss_accounts a WHERE a.id=alert_policies.account_key),''),COALESCE(group_id::text,''),COALESCE(device_id::text,''),COALESCE(customer_tier,''),enabled,fault_priorities,offline_after_seconds,steps FROM alert_policies ORDER BY scope,id`)
	if err != nil {
		return nil, fmt.Errorf("list alert policies: %w", err)
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		var p Policy
		var raw []byte
		var seconds int
		var stepRaw []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.Scope, &p.TenantID, &p.AccountKey, &p.CustomerID, &p.GroupID, &p.DeviceID, &p.CustomerTier, &p.Enabled, &raw, &seconds, &stepRaw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p.FaultPriorities); err != nil {
			return nil, err
		}
		p.OfflineAfter = time.Duration(seconds) * time.Second
		if err := json.Unmarshal(stepRaw, &p.Steps); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns one policy so API callers can authorize its target before a
// destructive operation. Policy IDs are otherwise opaque across tenants.
func (r *Repository) Get(ctx context.Context, id string) (*Policy, error) {
	var p Policy
	var priorities, steps []byte
	var seconds int
	err := r.db.QueryRowContext(ctx, `SELECT id,name,scope,COALESCE(tenant_id,''),COALESCE(account_key::text,''),COALESCE((SELECT a.customer_id::text FROM bss_accounts a WHERE a.id=alert_policies.account_key),''),COALESCE(group_id::text,''),COALESCE(device_id::text,''),COALESCE(customer_tier,''),enabled,fault_priorities,offline_after_seconds,steps FROM alert_policies WHERE id=$1`, id).Scan(&p.ID, &p.Name, &p.Scope, &p.TenantID, &p.AccountKey, &p.CustomerID, &p.GroupID, &p.DeviceID, &p.CustomerTier, &p.Enabled, &priorities, &seconds, &steps)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(priorities, &p.FaultPriorities); err != nil {
		return nil, err
	}
	p.OfflineAfter = time.Duration(seconds) * time.Second
	if err := json.Unmarshal(steps, &p.Steps); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM alert_policies WHERE id=$1", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) GroupIDsForDevice(ctx context.Context, deviceID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT group_id::text FROM device_group_members WHERE device_id=$1`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list device alert groups: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) OfflineDevices(ctx context.Context, limit int) ([]OfflineDevice, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT m.account_id, m.account_key::text, d.id::text, COALESCE(m.service_plan,''), d.last_inform_at FROM devices d JOIN account_device_mappings m ON m.device_id=d.id WHERE d.online_status IN ('OFFLINE','UNREACHABLE') ORDER BY d.last_inform_at NULLS FIRST LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list offline devices: %w", err)
	}
	defer rows.Close()
	var out []OfflineDevice
	for rows.Next() {
		var d OfflineDevice
		if err := rows.Scan(&d.TenantID, &d.AccountKey, &d.DeviceID, &d.CustomerTier, &d.LastInformAt); err != nil {
			return nil, err
		}
		d.GroupIDs, err = r.GroupIDsForDevice(ctx, d.DeviceID)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SIMWatchDevice is a device whose SIM was replaced and not yet approved. The
// hints are masked; no full identifier is ever read here.
type SIMWatchDevice struct {
	TenantID, AccountKey, DeviceID, CustomerTier                             string
	State, Network, ICCIDHint, IMSIHint, PreviousICCIDHint, PreviousIMSIHint string
	DetectedAt                                                               time.Time
	GroupIDs                                                                 []string
}

// SIMWatchDevices lists devices with an open SIM verdict that belong to an
// account (the account is the incident's tenant), alerts first, then oldest
// first, one page at a time so no verdict is starved by a long backlog. A
// device with no customer mapping is not listed: it gets the banner and the
// event but no incident.
func (r *Repository) SIMWatchDevices(ctx context.Context, limit, offset int) ([]SIMWatchDevice, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT m.account_id, COALESCE(m.account_key::text,''), d.id::text, COALESCE(m.service_plan,''), w.state, w.network, w.iccid_hint, w.imsi_hint, w.previous_iccid_hint, w.previous_imsi_hint, COALESCE(w.detected_at, w.last_checked_at) FROM device_sim_watch w JOIN devices d ON d.id=w.device_id JOIN account_device_mappings m ON m.device_id=d.id AND m.unassigned_at IS NULL WHERE w.state IN ('notice','alert') ORDER BY (w.state = 'alert') DESC, w.detected_at NULLS FIRST, d.id, m.account_id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list SIM watch devices: %w", err)
	}
	defer rows.Close()
	var out []SIMWatchDevice
	for rows.Next() {
		var d SIMWatchDevice
		if err := rows.Scan(&d.TenantID, &d.AccountKey, &d.DeviceID, &d.CustomerTier, &d.State, &d.Network, &d.ICCIDHint, &d.IMSIHint, &d.PreviousICCIDHint, &d.PreviousIMSIHint, &d.DetectedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].GroupIDs, err = r.GroupIDsForDevice(ctx, out[i].DeviceID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Mapping returns the service plan and the account's registry key for a
// device mapped to an account ID. The device settles which account the ID
// means when two customers use the same one.
func (r *Repository) Mapping(ctx context.Context, tenantID, deviceID string) (tier, accountKey string, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT COALESCE(service_plan,''), account_key::text FROM account_device_mappings WHERE account_id=$1 AND device_id=$2 LIMIT 1`, tenantID, deviceID).Scan(&tier, &accountKey)
	if err != nil && err != sql.ErrNoRows {
		return "", "", fmt.Errorf("lookup device mapping: %w", err)
	}
	return tier, accountKey, nil
}
