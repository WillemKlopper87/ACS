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
//
// supportedProtocolVersions is the agent's AgentSupportedProtocolVersions,
// already split on its comma separator by the caller; nil/empty means the
// caller has no version data for this link (e.g. the probe-fallback path,
// whose GetResp carries no such field). In that case the existing value is
// left untouched rather than being blanked out — the CASE in the ON
// CONFLICT clause below only overwrites it when the new value is
// non-empty, the same "don't silently reset a column this call has no
// data for" discipline already applied to controller_role.
func (r *Repository) LinkUspAgent(ctx context.Context, deviceID, endpointID, mtpKind string, supportedProtocolVersions []string) error {
	// store.StringArray(nil).Value() encodes as SQL NULL, which the
	// column's NOT NULL constraint rejects on a fresh INSERT (the ON
	// CONFLICT UPDATE path never has this problem, since it never writes
	// EXCLUDED's value directly -- see the CASE below). A nil slice here
	// means "caller has no version data," not "clear the column," so
	// normalize it to an empty (non-null) array either way.
	if supportedProtocolVersions == nil {
		supportedProtocolVersions = []string{}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO usp_agents (device_id, endpoint_id, mtp_kind, connected, last_connected_at, last_seen_at, supported_protocol_versions)
		VALUES ($1, $2, $3, true, now(), now(), $4)
		ON CONFLICT (device_id) DO UPDATE SET
			endpoint_id = EXCLUDED.endpoint_id,
			mtp_kind = EXCLUDED.mtp_kind,
			connected = true,
			last_connected_at = now(),
			last_seen_at = now(),
			supported_protocol_versions = CASE WHEN cardinality(EXCLUDED.supported_protocol_versions) > 0
				THEN EXCLUDED.supported_protocol_versions
				ELSE usp_agents.supported_protocol_versions END
	`, deviceID, endpointID, mtpKind, store.StringArray(supportedProtocolVersions))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "usp_agents_endpoint_id_key" {
			return fmt.Errorf("%w: %s", ErrEndpointIDInUse, endpointID)
		}
		return fmt.Errorf("link usp agent: %w", err)
	}
	return nil
}

// MarkUspAgentDisconnected records that a device's live USP session on
// endpointID ended. It is scoped to endpointID, not just deviceID: a
// device can reconnect under a NEW endpoint id while the OLD endpoint id's
// connection is still tearing down elsewhere (TestLinkUspAgentReconnect
// covers the reconnect itself). If that stale old-endpoint disconnect
// fires after the reconnect's LinkUspAgent has already retargeted the
// single per-device row to the new endpoint id, this WHERE clause makes it
// a no-op instead of clobbering the still-live session — endpoint_id in
// the row no longer matches endpointID, so nothing is updated.
//
// A no-op (not an error) either way: there being no matching row (unknown
// device, or a superseded endpoint id) is not itself a failure.
func (r *Repository) MarkUspAgentDisconnected(ctx context.Context, deviceID, endpointID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE usp_agents SET connected = false, last_seen_at = now()
		WHERE device_id = $1 AND endpoint_id = $2
	`, deviceID, endpointID)
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
