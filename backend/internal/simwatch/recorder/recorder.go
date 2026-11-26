// Package recorder is the ACS's only writer of device_sim_watch. It is kept
// apart from simwatch so that only a service that imports it needs INSERT and
// UPDATE on the table.
package recorder

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"acs/internal/simwatch"
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

// LastChecked is when a reply last carried the device's SIM identity; the
// zero time when the device has no row yet.
func (r *Repository) LastChecked(ctx context.Context, deviceID string) (time.Time, error) {
	var at time.Time
	err := r.db.QueryRowContext(ctx, `SELECT last_checked_at FROM device_sim_watch WHERE device_id=$1`, deviceID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read sim watch: %w", err)
	}
	return at, nil
}

// Record stores a verdict. cur is the SIM now, prev the one before; only
// masked hints reach the table. It is an idempotent upsert: an identical
// repeat of an open verdict keeps its detection time and approval state.
func (r *Repository) Record(ctx context.Context, deviceID string, v simwatch.Verdict, cur, prev simwatch.Identity, at time.Time) error {
	curICCID, curIMSI := simwatch.Hints(cur)
	prevICCID, prevIMSI := simwatch.Hints(prev)
	var err error
	switch v.Kind {
	case simwatch.KindNone:
		_, err = r.db.ExecContext(ctx, `UPDATE device_sim_watch SET last_checked_at=$2 WHERE device_id=$1`, deviceID, at)
	case simwatch.KindBaseline:
		// A device that already has an open verdict keeps it: only a person
		// moves it back to ok.
		_, err = r.db.ExecContext(ctx, `
			INSERT INTO device_sim_watch (device_id, state, network, iccid_hint, imsi_hint, last_checked_at)
			VALUES ($1, 'ok', $2, $3, $4, $5)
			ON CONFLICT (device_id) DO UPDATE SET
				network = CASE WHEN device_sim_watch.state = 'ok' THEN EXCLUDED.network ELSE device_sim_watch.network END,
				iccid_hint = CASE WHEN device_sim_watch.state = 'ok' THEN EXCLUDED.iccid_hint ELSE device_sim_watch.iccid_hint END,
				imsi_hint = CASE WHEN device_sim_watch.state = 'ok' THEN EXCLUDED.imsi_hint ELSE device_sim_watch.imsi_hint END,
				last_checked_at = EXCLUDED.last_checked_at`,
			deviceID, v.Network, curICCID, curIMSI, at)
	case simwatch.KindNotice, simwatch.KindAlert:
		_, err = r.db.ExecContext(ctx, `
			INSERT INTO device_sim_watch (device_id, state, network, iccid_hint, imsi_hint, previous_iccid_hint, previous_imsi_hint, detected_at, last_checked_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
			ON CONFLICT (device_id) DO UPDATE SET
				state = EXCLUDED.state,
				network = EXCLUDED.network,
				iccid_hint = EXCLUDED.iccid_hint,
				imsi_hint = EXCLUDED.imsi_hint,
				previous_iccid_hint = EXCLUDED.previous_iccid_hint,
				previous_imsi_hint = EXCLUDED.previous_imsi_hint,
				detected_at = CASE WHEN device_sim_watch.state = EXCLUDED.state AND device_sim_watch.iccid_hint = EXCLUDED.iccid_hint
					AND device_sim_watch.imsi_hint = EXCLUDED.imsi_hint THEN device_sim_watch.detected_at ELSE EXCLUDED.detected_at END,
				approved_at = CASE WHEN device_sim_watch.state = EXCLUDED.state AND device_sim_watch.iccid_hint = EXCLUDED.iccid_hint
					AND device_sim_watch.imsi_hint = EXCLUDED.imsi_hint THEN device_sim_watch.approved_at ELSE NULL END,
				approved_by = CASE WHEN device_sim_watch.state = EXCLUDED.state AND device_sim_watch.iccid_hint = EXCLUDED.iccid_hint
					AND device_sim_watch.imsi_hint = EXCLUDED.imsi_hint THEN device_sim_watch.approved_by ELSE NULL END,
				approval_reason = CASE WHEN device_sim_watch.state = EXCLUDED.state AND device_sim_watch.iccid_hint = EXCLUDED.iccid_hint
					AND device_sim_watch.imsi_hint = EXCLUDED.imsi_hint THEN device_sim_watch.approval_reason ELSE NULL END,
				last_checked_at = EXCLUDED.last_checked_at`,
			deviceID, string(v.Kind), v.Network, curICCID, curIMSI, prevICCID, prevIMSI, at)
	default:
		return fmt.Errorf("record sim watch: unknown verdict %q", v.Kind)
	}
	if err != nil {
		return fmt.Errorf("record sim watch: %w", err)
	}
	return nil
}
