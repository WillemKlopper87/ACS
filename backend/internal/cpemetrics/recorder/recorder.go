// Package recorder is the only writer of device_cpe_samples. It is kept
// apart from cpemetrics so that only a service that imports it needs INSERT
// on the table.
package recorder

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"acs/internal/cpemetrics"
)

type Recorder struct{ db *sql.DB }

func New(db *sql.DB) *Recorder { return &Recorder{db: db} }

// Record stores one reading taken at `at`; a reading already stored for
// the same device and time is left as it is.
func (r *Recorder) Record(ctx context.Context, deviceID string, at time.Time, s cpemetrics.Sample) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO device_cpe_samples (device_id, sampled_at, rsrp, rsrq, snr, rssi, band, rat)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (device_id, sampled_at) DO NOTHING`,
		deviceID, at, s.RSRP, s.RSRQ, s.SNR, s.RSSI, s.Band, s.RAT)
	if err != nil {
		return fmt.Errorf("insert cpe sample: %w", err)
	}
	return nil
}
