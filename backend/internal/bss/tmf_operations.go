package bss

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type AlarmRecord struct {
	ID, SourceKey, AccountID, DeviceID, ServiceID, AlarmType, Severity, State, ProbableCause, SpecificProblem string
	RaisedAt                                                                                                  time.Time
	ClearedAt                                                                                                 *time.Time
	Details                                                                                                   json.RawMessage
}
type EventRecord struct {
	ID, SourceKey, AccountID, DeviceID, ServiceID, EventType string
	EventTime                                                time.Time
	Payload                                                  json.RawMessage
}
type ServiceProblemRecord struct {
	ID, ExternalID, AccountID, ServiceID, Status, Priority, ProblemType, Description string
	RelatedAlarmID                                                                   string
	CreatedAt                                                                        time.Time
	ResolvedAt                                                                       *time.Time
	Resolution                                                                       string
}

func (r *Repository) CreateEvent(ctx context.Context, sourceKey, accountID, deviceID, serviceID, eventType string, payload json.RawMessage, at time.Time) (*EventRecord, error) {
	id := uuid.New().String()
	var e EventRecord
	err := r.db.QueryRowContext(ctx, `INSERT INTO tmf_events (id,source_key,account_id,device_id,service_id,event_type,event_time,payload) VALUES ($1,$2,NULLIF($3,''),NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,$8) ON CONFLICT (source_key) DO NOTHING RETURNING id,source_key,COALESCE(account_id,''),COALESCE(device_id::text,''),COALESCE(service_id::text,''),event_type,event_time,payload`, id, sourceKey, accountID, deviceID, serviceID, eventType, at, payload).Scan(&e.ID, &e.SourceKey, &e.AccountID, &e.DeviceID, &e.ServiceID, &e.EventType, &e.EventTime, &e.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create TMF event: %w", err)
	}
	return &e, nil
}

func (r *Repository) CreateAlarm(ctx context.Context, sourceKey, accountID, deviceID, serviceID, alarmType, severity, cause, problem string, details json.RawMessage) (*AlarmRecord, error) {
	id := uuid.New().String()
	var a AlarmRecord
	err := r.db.QueryRowContext(ctx, `INSERT INTO tmf_alarms (id,source_key,account_id,device_id,service_id,alarm_type,perceived_severity,probable_cause,specific_problem,details) VALUES ($1,$2,NULLIF($3,''),NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,$8,$9,$10) ON CONFLICT (source_key) DO NOTHING RETURNING id,source_key,COALESCE(account_id,''),COALESCE(device_id::text,''),COALESCE(service_id::text,''),alarm_type,perceived_severity,state,COALESCE(probable_cause,''),COALESCE(specific_problem,''),raised_at,cleared_at,details`, id, sourceKey, accountID, deviceID, serviceID, alarmType, severity, cause, problem, details).Scan(&a.ID, &a.SourceKey, &a.AccountID, &a.DeviceID, &a.ServiceID, &a.AlarmType, &a.Severity, &a.State, &a.ProbableCause, &a.SpecificProblem, &a.RaisedAt, &a.ClearedAt, &a.Details)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create TMF alarm: %w", err)
	}
	return &a, nil
}

func (r *Repository) CreateServiceProblem(ctx context.Context, externalID, accountID, serviceID, problemType, description, priority, alarmID string) (*ServiceProblemRecord, error) {
	id := uuid.New().String()
	var p ServiceProblemRecord
	err := r.db.QueryRowContext(ctx, `INSERT INTO tmf_service_problems (id,external_id,account_id,service_id,problem_type,description,priority,related_alarm_id) VALUES ($1,NULLIF($2,''),$3,NULLIF($4,'')::uuid,$5,$6,NULLIF($7,''),NULLIF($8,'')::uuid) RETURNING id,COALESCE(external_id,''),account_id,COALESCE(service_id::text,''),status,COALESCE(priority,''),problem_type,description,COALESCE(related_alarm_id::text,''),created_at,resolved_at,COALESCE(resolution,'')`, id, externalID, accountID, serviceID, problemType, description, priority, alarmID).Scan(&p.ID, &p.ExternalID, &p.AccountID, &p.ServiceID, &p.Status, &p.Priority, &p.ProblemType, &p.Description, &p.RelatedAlarmID, &p.CreatedAt, &p.ResolvedAt, &p.Resolution)
	if err != nil {
		return nil, fmt.Errorf("create service problem: %w", err)
	}
	return &p, nil
}

func (r *Repository) FindServiceProblem(ctx context.Context, id string) (*ServiceProblemRecord, error) {
	var p ServiceProblemRecord
	err := r.db.QueryRowContext(ctx, `SELECT id,COALESCE(external_id,''),account_id,COALESCE(service_id::text,''),status,COALESCE(priority,''),problem_type,description,COALESCE(related_alarm_id::text,''),created_at,resolved_at,COALESCE(resolution,'') FROM tmf_service_problems WHERE id=$1 OR external_id=$1`, id).Scan(&p.ID, &p.ExternalID, &p.AccountID, &p.ServiceID, &p.Status, &p.Priority, &p.ProblemType, &p.Description, &p.RelatedAlarmID, &p.CreatedAt, &p.ResolvedAt, &p.Resolution)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find service problem: %w", err)
	}
	return &p, nil
}

func (r *Repository) UpdateServiceProblemStatus(ctx context.Context, id, status, resolution string) error {
	var resolved any
	if status == "resolved" || status == "closed" {
		resolved = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE tmf_service_problems SET status=$2,resolution=NULLIF($3,''),resolved_at=$4 WHERE id=$1`, id, status, resolution, resolved)
	if err != nil {
		return fmt.Errorf("update service problem: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
