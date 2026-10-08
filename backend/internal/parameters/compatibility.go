package parameters

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type InstanceEvidence struct {
	Path         string    `json:"path"`
	Firmware     string    `json:"firmware"`
	Instances    []string  `json:"instances"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

func Firmware(cache map[string]CachedValue) string {
	for _, path := range []string{"Device.DeviceInfo.SoftwareVersion", "InternetGatewayDevice.DeviceInfo.SoftwareVersion"} {
		if v := cache[path].Value; v != "" {
			return v
		}
	}
	return ""
}
func (r *Repository) SaveInstances(ctx context.Context, deviceID, path, firmware string, instances []string) error {
	if len(instances) > 2048 {
		return fmt.Errorf("instance discovery exceeds 2048 rows per table")
	}
	for _, instance := range instances {
		if !strings.HasPrefix(instance, path) || !strings.HasSuffix(instance, ".") || strings.Contains(instance, "{i}") {
			return fmt.Errorf("invalid instantiated path")
		}
		row := strings.TrimSuffix(strings.TrimPrefix(instance, path), ".")
		number, err := strconv.ParseUint(row, 10, 32)
		if err != nil || number == 0 {
			return fmt.Errorf("GetInstances returned a non-immediate or invalid row")
		}
	}
	raw, err := json.Marshal(instances)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT id FROM devices WHERE id=$1 FOR UPDATE`, deviceID); err != nil {
		return err
	}
	var cacheRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT parameters FROM device_parameter_cache WHERE device_id=$1`, deviceID).Scan(&cacheRaw); err != nil && err != sql.ErrNoRows {
		return err
	}
	var cache map[string]CachedValue
	if len(cacheRaw) > 0 {
		if err := json.Unmarshal(cacheRaw, &cache); err != nil {
			return err
		}
	}
	if Firmware(cache) != firmware {
		return ErrFirmwareChanged
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_instance_evidence(device_id,requested_path,firmware,instances) VALUES($1,$2,$3,$4) ON CONFLICT(device_id,requested_path) DO UPDATE SET firmware=EXCLUDED.firmware,instances=EXCLUDED.instances,discovered_at=now()`, deviceID, path, firmware, raw); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM device_instance_evidence WHERE device_id=$1 AND requested_path NOT IN(SELECT requested_path FROM device_instance_evidence WHERE device_id=$1 ORDER BY discovered_at DESC LIMIT 100)`, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *Repository) Instances(ctx context.Context, deviceID string) ([]InstanceEvidence, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT requested_path,firmware,instances,discovered_at FROM device_instance_evidence WHERE device_id=$1 ORDER BY requested_path`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstanceEvidence{}
	for rows.Next() {
		var v InstanceEvidence
		var raw []byte
		if err := rows.Scan(&v.Path, &v.Firmware, &raw, &v.DiscoveredAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &v.Instances); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *Repository) RecordOperation(ctx context.Context, deviceID, key, operation string, success bool, paths []string, detail any, expectedFirmware ...string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT id FROM devices WHERE id=$1 FOR UPDATE`, deviceID); err != nil {
		return err
	}
	var cacheRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT parameters FROM device_parameter_cache WHERE device_id=$1`, deviceID).Scan(&cacheRaw)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	var cache map[string]CachedValue
	if len(cacheRaw) > 0 {
		if err := json.Unmarshal(cacheRaw, &cache); err != nil {
			return err
		}
	}
	firmware := Firmware(cache)
	if len(expectedFirmware) > 0 && expectedFirmware[0] != firmware {
		return ErrFirmwareChanged
	}
	raw, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	notes, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO device_operation_evidence(device_id,command_key,firmware,operation,success,paths,detail) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(device_id,command_key) DO NOTHING`, deviceID, key, firmware, operation, success, raw, notes)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM device_operation_evidence WHERE device_id=$1 AND command_key NOT IN (SELECT command_key FROM device_operation_evidence WHERE device_id=$1 ORDER BY tested_at DESC LIMIT 100)`, deviceID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (r *Repository) OperationSummary(ctx context.Context, deviceID, firmware string) (map[string]bool, error) {
	out := map[string]bool{"read_tested": false, "write_tested": false}
	rows, err := r.db.QueryContext(ctx, `SELECT operation,bool_or(success) FROM device_operation_evidence WHERE device_id=$1 AND firmware=$2 GROUP BY operation`, deviceID, firmware)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var operation string
		var success bool
		if err := rows.Scan(&operation, &success); err != nil {
			return nil, err
		}
		if operation == "READ" {
			out["read_tested"] = success
		}
		if operation == "WRITE" {
			out["write_tested"] = success
		}
	}
	return out, rows.Err()
}
