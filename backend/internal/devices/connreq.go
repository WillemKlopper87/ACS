// Connection-request, data-model-root, and STUN bookkeeping on the
// device row (split out of repository.go, audit P3.1).
package devices

import (
	"acs/internal/store"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// UpdateConnectionRequestURL records the ManagementServer.ConnectionRequestURL
// a device reported on Inform (build plan §4 Phase 3). Called on every
// Inform that carries the parameter; a no-op write when it hasn't
// changed is fine — Inform-frequency writes to one row are not a
// meaningful cost here.
func (r *Repository) UpdateConnectionRequestURL(ctx context.Context, deviceID, url string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET connection_request_url = $2, last_updated_at = now()
		WHERE id = $1
	`, deviceID, url)
	if err != nil {
		return fmt.Errorf("update connection request url: %w", err)
	}
	return nil
}

// UpdateDataModelRoot records the data model root confirmed by a successful
// parameter discovery run (build plan nice-to-have backlog) — the first
// time this column moves off UNKNOWN in production, since UpsertFromInform
// deliberately never touches it (see that method's doc comment).
func (r *Repository) UpdateDataModelRoot(ctx context.Context, deviceID, root string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET data_model_root = $2, data_model_root_confirmed_at = now(), last_updated_at = now()
		WHERE id = $1
	`, deviceID, root)
	if err != nil {
		return fmt.Errorf("update data model root: %w", err)
	}
	return nil
}

// UpdateSTUNStatus records what a device reported about its own STUN/NAT
// state on Inform (critical feature backlog: STUN NAT traversal). When the
// CPE itself reports NATDetected=true, connection_request_mode is also
// classified as STUN_ANNEX_G — a real device behind CGNAT reporting a
// working STUN binding is a strictly better-informed status than
// UNKNOWN/PERIODIC_FALLBACK, so it's fine to overwrite those; a mode of
// DIRECT_IPV4/DIRECT_IPV6 (a Connection Request that already succeeded
// directly) is left untouched since direct reachability is strictly better
// than STUN and shouldn't be downgraded by this.
func (r *Repository) UpdateSTUNStatus(ctx context.Context, deviceID, udpConnectionRequestAddress string, natDetected bool) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET
			udp_connection_request_address = $2,
			nat_detected = $3,
			connection_request_mode = CASE
				WHEN $3 AND connection_request_mode NOT IN ('DIRECT_IPV4', 'DIRECT_IPV6') THEN 'STUN_ANNEX_G'
				ELSE connection_request_mode
			END,
			last_updated_at = now()
		WHERE id = $1
	`, deviceID, nullIfEmpty(udpConnectionRequestAddress), natDetected)
	if err != nil {
		return fmt.Errorf("update stun status: %w", err)
	}
	return nil
}

// RecordConnectionRequestAttempt records the outcome of a Connection
// Request attempt (design doc v3 §12.3). mode is optional — pass "" to
// leave the device's current reachability classification untouched
// (e.g. on a single transient HTTP failure, where downgrading the mode
// would just cause flapping — see build plan Phase 3 design notes).
func (r *Repository) RecordConnectionRequestAttempt(ctx context.Context, deviceID, status, mode string) error {
	if mode == "" {
		_, err := r.db.ExecContext(ctx, `
			UPDATE devices SET last_connection_request_at = now(), last_connection_request_status = $2
			WHERE id = $1
		`, deviceID, status)
		if err != nil {
			return fmt.Errorf("record connection request attempt: %w", err)
		}
		return nil
	}

	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET last_connection_request_at = now(), last_connection_request_status = $2, connection_request_mode = $3
		WHERE id = $1
	`, deviceID, status, mode)
	if err != nil {
		return fmt.Errorf("record connection request attempt: %w", err)
	}
	return nil
}

// MarkInformedAfterConnectionRequest records that a device sent an Inform
// (with CONNECTION REQUEST among its event codes) after a Connection
// Request attempt — the confirmation the connreq worker is waiting for.
func (r *Repository) MarkInformedAfterConnectionRequest(ctx context.Context, deviceID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET last_inform_after_connection_request_at = now() WHERE id = $1
	`, deviceID)
	if err != nil {
		return fmt.Errorf("mark informed after connection request: %w", err)
	}
	return nil
}

// InformedWithEventSince reports whether the device has sent an Inform
// carrying the given event code (e.g. "6 CONNECTION REQUEST") at or after
// the given time — how the connreq worker detects that its Connection
// Request actually provoked a new session, as opposed to a coincidental
// periodic Inform racing in during the wait window.
func (r *Repository) InformedWithEventSince(ctx context.Context, deviceID string, since time.Time, eventCodePrefix string) (bool, error) {
	var lastInformAt sql.NullTime
	var eventCodes store.StringArray
	err := r.db.QueryRowContext(ctx, `
		SELECT last_inform_at, last_inform_event_codes FROM devices WHERE id = $1
	`, deviceID).Scan(&lastInformAt, &eventCodes)
	if err != nil {
		return false, fmt.Errorf("check informed since: %w", err)
	}
	if !lastInformAt.Valid || lastInformAt.Time.Before(since) {
		return false, nil
	}
	for _, code := range eventCodes {
		if strings.HasPrefix(code, eventCodePrefix) {
			return true, nil
		}
	}
	return false, nil
}
