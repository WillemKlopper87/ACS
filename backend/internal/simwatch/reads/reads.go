// Package reads is the API's access to device_sim_watch: it serves the state
// and records an operator's approval. It is kept apart from simwatch and
// recorder so that only the API gets UPDATE for approvals.
package reads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// State is the SIM verdict for one device; hints are masked, never full
// identifiers.
type State struct {
	State             string
	Network           string
	ICCIDHint         string
	IMSIHint          string
	PreviousICCIDHint string
	PreviousIMSIHint  string
	DetectedAt        *time.Time
	ApprovedAt        *time.Time
	ApprovedBy        string
	ApprovalReason    string
}

// ErrNothingToApprove means the device has no SIM change awaiting approval.
var ErrNothingToApprove = errors.New("no SIM change awaiting approval")

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

// Get returns (nil, nil) when the device has no row.
func (r *Repository) Get(ctx context.Context, deviceID string) (*State, error) {
	var s State
	var by, reason sql.NullString
	var detected, approved sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT state, network, iccid_hint, imsi_hint, previous_iccid_hint, previous_imsi_hint,
		detected_at, approved_at, approved_by, approval_reason FROM device_sim_watch WHERE device_id = $1`, deviceID).
		Scan(&s.State, &s.Network, &s.ICCIDHint, &s.IMSIHint, &s.PreviousICCIDHint, &s.PreviousIMSIHint, &detected, &approved, &by, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sim watch: %w", err)
	}
	if detected.Valid {
		s.DetectedAt = &detected.Time
	}
	if approved.Valid {
		s.ApprovedAt = &approved.Time
	}
	s.ApprovedBy, s.ApprovalReason = by.String, reason.String
	return &s, nil
}

// Approve accepts the SIM now in the device. It never touches the CPE. seen
// is the detection time of the verdict the operator was shown: if the SIM
// changed again since (a new verdict has a new detection time) nothing is
// approved and ErrNothingToApprove is returned.
func (r *Repository) Approve(ctx context.Context, deviceID, actor, reason string, seen, at time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE device_sim_watch SET state = 'ok', approved_at = $2, approved_by = $3, approval_reason = $4,
			last_event_at = NULL, last_event_state = NULL
		WHERE device_id = $1 AND state IN ('notice', 'alert') AND detected_at = $5`, deviceID, at, actor, reason, seen)
	if err != nil {
		return fmt.Errorf("approve sim change: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return err
		}
		return ErrNothingToApprove
	}
	return nil
}
