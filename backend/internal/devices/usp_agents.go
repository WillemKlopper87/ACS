package devices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"acs/internal/store"
)

// UspAgent is a row of usp_agents — the live USP endpoint connection state
// for a devices row (migration 0053). One row per device: it is overwritten
// on reconnect, not appended to.
type UspAgent struct {
	DeviceID                  string
	EndpointID                string
	MTPKind                   string
	Connected                 bool
	LastConnectedAt           time.Time
	LastSeenAt                time.Time
	SupportedProtocolVersions []string
	ControllerRole            string
}

// ErrEndpointIDInUse is returned when the endpoint id being linked is
// already claimed by a different device's usp_agents row —
// usp_agents_endpoint_id_key firing on a device_id other than the one being
// written. This should not happen if the caller's identity reconciliation
// (UpsertFromOnBoard's oui_serial match) is correct, but the database must
// not silently corrupt state if it ever does.
var ErrEndpointIDInUse = errors.New("usp endpoint id already linked to a different device")

// ErrUspAgentNotFound is returned when no usp_agents row exists for a given
// endpoint id.
var ErrUspAgentNotFound = errors.New("no usp_agents row for endpoint id")

const uspAgentColumns = `device_id, endpoint_id, mtp_kind, connected, last_connected_at, last_seen_at, supported_protocol_versions, controller_role`

// LinkUspAgent creates (or, on reconnect, updates in place) the usp_agents
// row for a device — one row per device_id, keyed by the ON CONFLICT below.
// A conflict on endpoint_id instead (a different device already claiming
// this endpoint id) is the corruption case and surfaces as
// ErrEndpointIDInUse rather than a raw pg error.
func (r *Repository) LinkUspAgent(ctx context.Context, deviceID, endpointID, mtpKind string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO usp_agents (device_id, endpoint_id, mtp_kind, connected, last_connected_at, last_seen_at)
		VALUES ($1, $2, $3, true, now(), now())
		ON CONFLICT (device_id) DO UPDATE SET
			endpoint_id = EXCLUDED.endpoint_id,
			mtp_kind = EXCLUDED.mtp_kind,
			connected = true,
			last_connected_at = now(),
			last_seen_at = now()
	`, deviceID, endpointID, mtpKind)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "usp_agents_endpoint_id_key" {
			return fmt.Errorf("%w: %s", ErrEndpointIDInUse, endpointID)
		}
		return fmt.Errorf("link usp agent: %w", err)
	}
	return nil
}

// MarkUspAgentDisconnected records that a device's live USP session ended.
// A no-op (not an error) if no usp_agents row exists yet for that device —
// there is nothing to mark disconnected.
func (r *Repository) MarkUspAgentDisconnected(ctx context.Context, deviceID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE usp_agents SET connected = false, last_seen_at = now() WHERE device_id = $1
	`, deviceID)
	if err != nil {
		return fmt.Errorf("mark usp agent disconnected: %w", err)
	}
	return nil
}

// GetUspAgentByEndpointID looks up the usp_agents row for a live USP
// endpoint id — the endpoint-ID-parse fast path's lookup when a message
// arrives on an already-known connection.
func (r *Repository) GetUspAgentByEndpointID(ctx context.Context, endpointID string) (*UspAgent, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+uspAgentColumns+` FROM usp_agents WHERE endpoint_id = $1`, endpointID)
	agent, err := scanUspAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrUspAgentNotFound, endpointID)
	}
	if err != nil {
		return nil, err
	}
	return agent, nil
}

func scanUspAgent(s scanner) (*UspAgent, error) {
	var a UspAgent
	var supportedVersions store.StringArray
	var controllerRole sql.NullString
	if err := s.Scan(&a.DeviceID, &a.EndpointID, &a.MTPKind, &a.Connected,
		&a.LastConnectedAt, &a.LastSeenAt, &supportedVersions, &controllerRole); err != nil {
		return nil, fmt.Errorf("scan usp agent: %w", err)
	}
	a.SupportedProtocolVersions = []string(supportedVersions)
	if controllerRole.Valid {
		a.ControllerRole = controllerRole.String
	}
	return &a, nil
}
