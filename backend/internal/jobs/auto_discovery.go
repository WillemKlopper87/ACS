package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/google/uuid"
)

// QueueDiscoveryIfNeeded serializes per-device admission so reconnects cannot
// pile up duplicate queued discovery jobs.
func (r *Repository) QueueDiscoveryIfNeeded(ctx context.Context, deviceID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT id FROM devices WHERE id=$1 FOR UPDATE`, deviceID); err != nil {
		return err
	}
	var firmware string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(parameters->'Device.DeviceInfo.SoftwareVersion'->>'value',parameters->'InternetGatewayDevice.DeviceInfo.SoftwareVersion'->>'value','') FROM device_parameter_cache WHERE device_id=$1`, deviceID).Scan(&firmware)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	payload, _ := json.Marshal(ParameterDiscoveryPayload{Root: "Device.", FallbackRoot: "InternetGatewayDevice.", Incremental: true, Firmware: firmware})
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,command_key,device_id,type,status,payload,created_by,max_attempts) SELECT $1,$2,$3,'PARAMETER_DISCOVERY','QUEUED',$4,'system:onboarding-discovery',$5 WHERE NOT EXISTS(SELECT 1 FROM jobs WHERE device_id=$3 AND type='PARAMETER_DISCOVERY' AND status IN('QUEUED','RPC_SENT'))`, uuid.NewString(), newCommandKey(TypeParameterDiscovery), deviceID, payload, DiscoveryMaxObjects)
	if err != nil {
		return err
	}
	return tx.Commit()
}
