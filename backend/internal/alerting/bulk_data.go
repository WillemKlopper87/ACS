package alerting

import (
	"context"
	"encoding/json"
	"time"
)

type BulkDataTarget struct {
	TenantID, DeviceID, ReceiverID, CustomerTier string
	ExpiresAt                                    time.Time
	LastReportAt                                 *time.Time
	Interval                                     int
	GroupIDs                                     []string
}

func (r *Repository) MissingBulkData(ctx context.Context, limit int) ([]BulkDataTarget, error) {
	rows, err := r.db.QueryContext(ctx, `WITH latest AS (SELECT DISTINCT ON(device_id,profile_instance) * FROM bulk_data_receivers WHERE NOT revoked ORDER BY device_id,profile_instance,created_at DESC) SELECT m.account_id,r.device_id::text,r.id::text,COALESCE(m.service_plan,''),r.expires_at,r.last_report_at,r.reporting_interval FROM latest r JOIN account_device_mappings m ON m.device_id=r.device_id WHERE r.expires_at<=now() OR GREATEST(COALESCE(r.last_report_at,r.renewed_at),r.renewed_at)+(r.reporting_interval*3+60)*interval '1 second'<now() ORDER BY r.expires_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BulkDataTarget{}
	for rows.Next() {
		var v BulkDataTarget
		if err := rows.Scan(&v.TenantID, &v.DeviceID, &v.ReceiverID, &v.CustomerTier, &v.ExpiresAt, &v.LastReportAt, &v.Interval); err != nil {
			return nil, err
		}
		v.GroupIDs, err = r.GroupIDsForDevice(ctx, v.DeviceID)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *IncidentRepository) RecoverBulkData(ctx context.Context) ([]Incident, error) {
	rows, err := r.db.QueryContext(ctx, `UPDATE alert_incidents i SET state='recovered',recovered_at=now(),next_escalation_at=NULL WHERE i.condition_key LIKE 'bulk-data:%' AND i.state IN ('open','acknowledged','suppressed') AND NOT EXISTS(SELECT 1 FROM bulk_data_receivers r WHERE i.condition_key='bulk-data:'||r.id::text AND NOT r.revoked AND (r.expires_at<=now() OR GREATEST(COALESCE(r.last_report_at,r.renewed_at),r.renewed_at)+(r.reporting_interval*3+60)*interval '1 second'<now()) AND NOT EXISTS(SELECT 1 FROM bulk_data_receivers newer WHERE newer.device_id=r.device_id AND newer.profile_instance=r.profile_instance AND NOT newer.revoked AND newer.created_at>r.created_at)) RETURNING i.id,i.tenant_id,i.device_id::text,i.condition_key,i.priority,i.summary,i.state,i.escalation_stage,i.first_seen_at,i.last_seen_at,i.next_escalation_at,i.acknowledged_at,COALESCE(i.acknowledged_by,''),i.recovered_at,i.details`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Incident{}
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
