package jobs

import "context"

// HasOpenJobByCreator reports whether the device has a QUEUED or RPC_SENT
// job created by createdBy, so periodic system reads (e.g. Wi-Fi insights
// sampling) never stack while a device is offline.
func (r *Repository) HasOpenJobByCreator(ctx context.Context, deviceID, createdBy string) (bool, error) {
	var open bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE device_id=$1 AND created_by=$2 AND status IN ($3,$4))`,
		deviceID, createdBy, StatusQueued, StatusRPCSent).Scan(&open)
	return open, err
}
