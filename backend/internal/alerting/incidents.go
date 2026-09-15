package alerting

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrIncidentNotFound     = sql.ErrNoRows
	ErrInvalidIncidentState = fmt.Errorf("invalid incident state")
)

type IncidentState string

const (
	IncidentOpen         IncidentState = "open"
	IncidentAcknowledged IncidentState = "acknowledged"
	IncidentSuppressed   IncidentState = "suppressed"
	IncidentRecovered    IncidentState = "recovered"
	IncidentClosed       IncidentState = "closed"
)

type Incident struct {
	ID, TenantID, DeviceID, ConditionKey, Summary string
	Priority                                      Priority
	State                                         IncidentState
	EscalationStage                               int
	FirstSeenAt, LastSeenAt                       time.Time
	NextEscalationAt, AcknowledgedAt, RecoveredAt *time.Time
	AcknowledgedBy                                string
	Details                                       map[string]any
}

type IncidentRepository struct{ db *sql.DB }

func NewIncidentRepository(db *sql.DB) *IncidentRepository { return &IncidentRepository{db: db} }

func (r *IncidentRepository) Open(ctx context.Context, tenantID, deviceID, conditionKey string, priority Priority, summary string, next *time.Time, details map[string]any) (*Incident, error) {
	raw, err := json.Marshal(details)
	if err != nil {
		return nil, err
	}
	id := uuid.New().String()
	var out Incident
	var detailRaw []byte
	err = r.db.QueryRowContext(ctx, `INSERT INTO alert_incidents (id,tenant_id,device_id,condition_key,priority,summary,next_escalation_at,details) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tenant_id,device_id,condition_key) DO UPDATE SET priority=EXCLUDED.priority,summary=EXCLUDED.summary,last_seen_at=now(),next_escalation_at=COALESCE(alert_incidents.next_escalation_at,EXCLUDED.next_escalation_at),details=EXCLUDED.details RETURNING id,tenant_id,device_id::text,condition_key,priority,summary,state,escalation_stage,first_seen_at,last_seen_at,next_escalation_at,acknowledged_at,COALESCE(acknowledged_by,''),recovered_at,details`, id, tenantID, deviceID, conditionKey, priority, summary, next, raw).Scan(&out.ID, &out.TenantID, &out.DeviceID, &out.ConditionKey, &out.Priority, &out.Summary, &out.State, &out.EscalationStage, &out.FirstSeenAt, &out.LastSeenAt, &out.NextEscalationAt, &out.AcknowledgedAt, &out.AcknowledgedBy, &out.RecoveredAt, &detailRaw)
	if err != nil {
		return nil, fmt.Errorf("open alert incident: %w", err)
	}
	if json.Unmarshal(detailRaw, &out.Details) != nil {
		out.Details = map[string]any{}
	}
	return &out, nil
}

func (r *IncidentRepository) SetState(ctx context.Context, id, state, actor string) error {
	var q string
	switch IncidentState(state) {
	case IncidentAcknowledged:
		q = `UPDATE alert_incidents SET state='acknowledged',acknowledged_at=now(),acknowledged_by=$2 WHERE id=$1 AND state='open'`
	case IncidentSuppressed:
		q = `UPDATE alert_incidents SET state='suppressed' WHERE id=$1 AND state IN ('open','acknowledged')`
	case IncidentRecovered:
		q = `UPDATE alert_incidents SET state='recovered',recovered_at=COALESCE(recovered_at,now()) WHERE id=$1 AND state IN ('open','acknowledged','suppressed')`
	case IncidentClosed:
		q = `UPDATE alert_incidents SET state='closed' WHERE id=$1 AND state IN ('recovered','acknowledged','suppressed')`
	default:
		return ErrInvalidIncidentState
	}
	res, err := r.db.ExecContext(ctx, q, id, actor)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *IncidentRepository) List(ctx context.Context, state string) ([]Incident, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,tenant_id,device_id::text,condition_key,priority,summary,state,escalation_stage,first_seen_at,last_seen_at,next_escalation_at,acknowledged_at,COALESCE(acknowledged_by,''),recovered_at,details FROM alert_incidents WHERE ($1='' OR state=$1) ORDER BY last_seen_at DESC LIMIT 500`, state)
	if err != nil {
		return nil, fmt.Errorf("list alert incidents: %w", err)
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var i Incident
		var raw []byte
		if err := rows.Scan(&i.ID, &i.TenantID, &i.DeviceID, &i.ConditionKey, &i.Priority, &i.Summary, &i.State, &i.EscalationStage, &i.FirstSeenAt, &i.LastSeenAt, &i.NextEscalationAt, &i.AcknowledgedAt, &i.AcknowledgedBy, &i.RecoveredAt, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &i.Details)
		out = append(out, i)
	}
	return out, rows.Err()
}

func (r *IncidentRepository) Due(ctx context.Context, limit int) ([]Incident, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,tenant_id,device_id::text,condition_key,priority,summary,state,escalation_stage,first_seen_at,last_seen_at,next_escalation_at,acknowledged_at,COALESCE(acknowledged_by,''),recovered_at,details FROM alert_incidents WHERE state='open' AND next_escalation_at IS NOT NULL AND next_escalation_at <= now() ORDER BY next_escalation_at LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list due alert incidents: %w", err)
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var i Incident
		var raw []byte
		if err := rows.Scan(&i.ID, &i.TenantID, &i.DeviceID, &i.ConditionKey, &i.Priority, &i.Summary, &i.State, &i.EscalationStage, &i.FirstSeenAt, &i.LastSeenAt, &i.NextEscalationAt, &i.AcknowledgedAt, &i.AcknowledgedBy, &i.RecoveredAt, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &i.Details)
		out = append(out, i)
	}
	return out, rows.Err()
}

func (r *IncidentRepository) Advance(ctx context.Context, id string, next *time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE alert_incidents SET escalation_stage=escalation_stage+1,next_escalation_at=$2 WHERE id=$1 AND state='open' AND next_escalation_at <= now()`, id, next)
	if err != nil {
		return fmt.Errorf("advance alert incident: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
