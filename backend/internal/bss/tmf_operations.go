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
