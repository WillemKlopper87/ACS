package jobs

import (
	"acs/internal/datamodel"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// BindFirmware records the firmware before dispatch and preserves it across
// retries/readback. A changed firmware requires a new operation and discovery.
func (r *Repository) BindFirmware(ctx context.Context, job *Job) error {
	if job.Type != TypeGetParameter && job.Type != TypeSetParameter {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT id FROM devices WHERE id=$1 FOR UPDATE`, job.DeviceID); err != nil {
		return err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT parameters FROM device_parameter_cache WHERE device_id=$1`, job.DeviceID).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	var cache map[string]struct {
		Value string `json:"value"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cache); err != nil {
			return err
		}
	}
	firmware := cache["Device.DeviceInfo.SoftwareVersion"].Value
	if firmware == "" {
		firmware = cache["InternetGatewayDevice.DeviceInfo.SoftwareVersion"].Value
	}
	var payload map[string]any
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return err
	}
	if payload["evidence_bound"] == true {
		expected, _ := payload["evidence_firmware"].(string)
		if expected != firmware {
			return fmt.Errorf("firmware changed since operation began")
		}
		return nil
	}
	payload["evidence_bound"] = true
	payload["evidence_firmware"] = firmware
	if job.Type == TypeSetParameter {
		var capabilitiesRaw []byte
		if err := tx.QueryRowContext(ctx, `SELECT capabilities FROM device_parameter_names WHERE device_id=$1`, job.DeviceID).Scan(&capabilitiesRaw); err != nil && err != sql.ErrNoRows {
			return err
		}
		var capabilities []struct {
			Path   string `json:"path"`
			Access string `json:"access"`
		}
		if len(capabilitiesRaw) > 0 {
			if err := json.Unmarshal(capabilitiesRaw, &capabilities); err != nil {
				return err
			}
		}
		var write SetParameterPayload
		if err := json.Unmarshal(job.Payload, &write); err != nil {
			return err
		}
		skipped := []string{}
		for _, parameter := range write.Parameters {
			for _, capability := range capabilities {
				if capability.Access == "PARAM_WRITE_ONLY" && (capability.Path == parameter.Name || capability.Path == datamodel.NormalizePath(parameter.Name)) {
					skipped = append(skipped, parameter.Name)
				}
			}
		}
		payload["non_readable"] = skipped
	}
	next, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET payload=$2 WHERE id=$1 AND status='RPC_SENT' AND attempts=$3`, job.ID, next, job.Attempts)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("operation attempt is no longer current")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	job.Payload = next
	return nil
}
func EvidenceFirmware(job *Job) string {
	var p struct {
		Firmware string `json:"evidence_firmware"`
	}
	_ = json.Unmarshal(job.Payload, &p)
	return p.Firmware
}
