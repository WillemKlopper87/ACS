// Package observability holds cross-cutting concerns (audit logging here;
// metrics/tracing land in later phases, build plan §4 Phase 6/7).
package observability

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Auditor writes to the append-only audit_log table (design doc v3
// §11.8: "Audit storage should be append-only"). Phase 1 has no write
// RPCs or operator REST actions yet to audit (those arrive Phase 2+), so
// the only actor today is "system" recording CWMP session lifecycle.
type Auditor struct {
	db *sql.DB
}

func NewAuditor(db *sql.DB) *Auditor {
	return &Auditor{db: db}
}

// Record writes one audit event. deviceID may be empty for events not
// tied to a specific device. details is marshaled to JSONB as-is.
func (a *Auditor) Record(ctx context.Context, actor, deviceID, action string, details map[string]any) error {
	var detailsJSON []byte
	if details != nil {
		b, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("marshal audit details: %w", err)
		}
		detailsJSON = b
	}

	var deviceIDArg any
	if deviceID != "" {
		deviceIDArg = deviceID
	}

	_, err := a.db.ExecContext(ctx, `
		INSERT INTO audit_log (id, actor, device_id, action, details)
		VALUES ($1, $2, $3, $4, $5)
	`, uuid.New().String(), actor, deviceIDArg, action, detailsJSON)
	if err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}

// AuditEntry is a row of the append-only audit_log table.
type AuditEntry struct {
	ID         string
	OccurredAt time.Time
	Actor      string
	DeviceID   *string
	Action     string
	Details    json.RawMessage
}

// listLimit caps List the same way jobs.Repository's own listLimit does —
// this is an operator review screen, not a bulk export.
const listLimit = 300

// List returns the most recent audit entries, optionally filtered by
// device_id and/or action, newest first. Either filter empty means "any."
func (a *Auditor) List(ctx context.Context, deviceID, action string) ([]AuditEntry, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, occurred_at, actor, device_id, action, details
		FROM audit_log
		WHERE ($1 = '' OR device_id::text = $1)
		  AND ($2 = '' OR action = $2)
		ORDER BY occurred_at DESC
		LIMIT $3`, deviceID, action, listLimit)
	if err != nil {
		return nil, fmt.Errorf("list audit log: %w", err)
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var deviceID sql.NullString
		var details sql.NullString
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.Actor, &deviceID, &e.Action, &details); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		if deviceID.Valid {
			e.DeviceID = &deviceID.String
		}
		if details.Valid {
			e.Details = json.RawMessage(details.String)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
