package parameters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type DiscoverySnapshot struct {
	ExpectedFirmware string                `json:"-"`
	CommandKey       string                `json:"command_key"`
	Protocol         string                `json:"protocol"`
	Root             string                `json:"root"`
	Names            map[string]bool       `json:"names"`
	Capabilities     []SupportedCapability `json:"capabilities"`
	Evidence         map[string]any        `json:"evidence"`
	DiscoveredAt     time.Time             `json:"discovered_at"`
}

var ErrFirmwareChanged = errors.New("firmware changed during discovery; completed tree cannot be published")

// SaveDiscovery atomically publishes a completed snapshot and its identity /
// firmware evidence. A failed or partial walk never replaces the last tree.
func (r *Repository) SaveDiscovery(ctx context.Context, deviceID string, s DiscoverySnapshot) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var manufacturer, model, oui, serial string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(manufacturer,''),COALESCE(product_class,''),COALESCE(oui,''),COALESCE(serial_number,'') FROM devices WHERE id=$1 FOR UPDATE`, deviceID).Scan(&manufacturer, &model, &oui, &serial); err != nil {
		return err
	}
	var firmware string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(parameters->'Device.DeviceInfo.SoftwareVersion'->>'value',parameters->'InternetGatewayDevice.DeviceInfo.SoftwareVersion'->>'value','') FROM device_parameter_cache WHERE device_id=$1`, deviceID).Scan(&firmware); err != nil && err != sql.ErrNoRows {
		return err
	}
	if s.ExpectedFirmware != "" && s.ExpectedFirmware != firmware {
		return ErrFirmwareChanged
	}
	s.Evidence = map[string]any{"manufacturer": manufacturer, "model": model, "oui": oui, "serial_number": serial, "firmware": firmware}
	names, err := json.Marshal(s.Names)
	if err != nil {
		return err
	}
	if s.Capabilities == nil {
		s.Capabilities = []SupportedCapability{}
	}
	caps, err := json.Marshal(s.Capabilities)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(s.Evidence)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO device_discovery_snapshots(device_id,command_key,protocol,root,names,capabilities,evidence) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(device_id,command_key) DO NOTHING`, deviceID, s.CommandKey, s.Protocol, s.Root, names, caps, evidence); err != nil {
		return fmt.Errorf("archive discovery: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO device_parameter_names(device_id,names,capabilities,evidence,discovered_at) VALUES($1,$2,$3,$4,now()) ON CONFLICT(device_id) DO UPDATE SET names=EXCLUDED.names,capabilities=EXCLUDED.capabilities,evidence=EXCLUDED.evidence,discovered_at=now()`, deviceID, names, caps, evidence); err != nil {
		return err
	}
	// Keep a bounded history per device, rather than accumulating full trees forever.
	if _, err = tx.ExecContext(ctx, `DELETE FROM device_discovery_snapshots WHERE device_id=$1 AND id NOT IN (SELECT id FROM device_discovery_snapshots WHERE device_id=$1 ORDER BY discovered_at DESC,id DESC LIMIT 20)`, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) DiscoverySnapshots(ctx context.Context, deviceID string) ([]DiscoverySnapshot, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT command_key,protocol,root,names,capabilities,evidence,discovered_at FROM device_discovery_snapshots WHERE device_id=$1 ORDER BY discovered_at DESC,id DESC LIMIT 20`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiscoverySnapshot{}
	for rows.Next() {
		var s DiscoverySnapshot
		var names, caps, evidence []byte
		if err := rows.Scan(&s.CommandKey, &s.Protocol, &s.Root, &names, &caps, &evidence, &s.DiscoveredAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(names, &s.Names); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(caps, &s.Capabilities); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(evidence, &s.Evidence); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
