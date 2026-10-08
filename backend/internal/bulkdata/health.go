package bulkdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/google/uuid"
	"time"
)

type Health struct {
	ReceiverID      string     `json:"receiver_id"`
	ProfileInstance int        `json:"profile_instance"`
	IntervalSeconds int        `json:"interval_seconds"`
	LastReportAt    *time.Time `json:"last_report_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	RenewedAt       time.Time  `json:"renewed_at"`
	Revoked         bool       `json:"revoked"`
	State           string     `json:"state"`
	Alert           string     `json:"alert,omitempty"`
}

func EvaluateHealth(h Health, now time.Time) Health {
	switch {
	case h.Revoked:
		h.State = "revoked"
	case !h.ExpiresAt.After(now):
		h.State = "expired"
		h.Alert = "Renew receiver credentials to resume collection."
	default:
		last := h.RenewedAt
		if h.LastReportAt != nil && h.LastReportAt.After(last) {
			last = *h.LastReportAt
		}
		if now.Sub(last) > time.Duration(3*h.IntervalSeconds+60)*time.Second {
			h.State = "missing_reports"
			h.Alert = "No report for three reporting intervals. Check profile enable state, credentials, upload reachability and device clock."
		} else if h.LastReportAt == nil {
			h.State = "awaiting_first_report"
		} else {
			h.State = "receiving"
		}
		if h.ExpiresAt.Sub(now) < 24*time.Hour && h.Alert == "" {
			h.Alert = "Receiver expires within 24 hours; renew credentials."
		}
	}
	return h
}
func (r *Repository) Health(ctx context.Context, deviceID string, now time.Time) ([]Health, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,profile_instance,reporting_interval,last_report_at,expires_at,renewed_at,revoked FROM bulk_data_receivers WHERE device_id=$1 ORDER BY renewed_at DESC LIMIT 100`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Health{}
	for rows.Next() {
		var h Health
		if err := rows.Scan(&h.ReceiverID, &h.ProfileInstance, &h.IntervalSeconds, &h.LastReportAt, &h.ExpiresAt, &h.RenewedAt, &h.Revoked); err != nil {
			return nil, err
		}
		out = append(out, EvaluateHealth(h, now))
	}
	return out, rows.Err()
}

// RenewAndQueue atomically extends the receiver and queues the new credential
// on its device. A crash cannot leave a renewal without a provisioning job.
func (r *Repository) RenewAndQueue(ctx context.Context, receiver Receiver, expires time.Time, payload any, operator string) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE bulk_data_receivers SET expires_at=$3,renewed_at=now() WHERE id=$1 AND device_id=$2 AND NOT revoked`, receiver.ID, receiver.DeviceID, expires)
	if err != nil {
		return "", err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if count != 1 {
		return "", sql.ErrNoRows
	}
	key := "bulk-renew-" + uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,command_key,device_id,type,payload,created_by) VALUES($1,$2,$3,'SET_PARAMETER',$4,$5)`, uuid.NewString(), key, receiver.DeviceID, raw, operator); err != nil {
		return "", err
	}
	return key, tx.Commit()
}
