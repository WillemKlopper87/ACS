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
	RelatedEventIDs, AffectedResourceIDs                                             []string
	Impact, Severity, RootCause                                                      string
	ResolutionDate                                                                   *time.Time
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

func (r *Repository) FindEvent(ctx context.Context, id string) (*EventRecord, error) {
	return r.findEvent(ctx, id, "")
}
func (r *Repository) FindEventForAccount(ctx context.Context, id, accountID string) (*EventRecord, error) {
	return r.findEvent(ctx, id, accountID)
}
func (r *Repository) findEvent(ctx context.Context, id, accountID string) (*EventRecord, error) {
	var e EventRecord
	err := r.db.QueryRowContext(ctx, `SELECT id,source_key,COALESCE(account_id,''),COALESCE(device_id::text,''),COALESCE(service_id::text,''),event_type,event_time,payload FROM tmf_events WHERE (id::text=$1 OR source_key=$1) AND ($2='' OR account_id=$2)`, id, accountID).Scan(&e.ID, &e.SourceKey, &e.AccountID, &e.DeviceID, &e.ServiceID, &e.EventType, &e.EventTime, &e.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find TMF event: %w", err)
	}
	return &e, nil
}

func (r *Repository) FindAlarm(ctx context.Context, id string) (*AlarmRecord, error) {
	return r.findAlarm(ctx, id, "")
}
func (r *Repository) FindAlarmForAccount(ctx context.Context, id, accountID string) (*AlarmRecord, error) {
	return r.findAlarm(ctx, id, accountID)
}
func (r *Repository) findAlarm(ctx context.Context, id, accountID string) (*AlarmRecord, error) {
	var a AlarmRecord
	err := r.db.QueryRowContext(ctx, `SELECT id,source_key,COALESCE(account_id,''),COALESCE(device_id::text,''),COALESCE(service_id::text,''),alarm_type,perceived_severity,state,COALESCE(probable_cause,''),COALESCE(specific_problem,''),raised_at,cleared_at,details FROM tmf_alarms WHERE (id::text=$1 OR source_key=$1) AND ($2='' OR account_id=$2)`, id, accountID).Scan(&a.ID, &a.SourceKey, &a.AccountID, &a.DeviceID, &a.ServiceID, &a.AlarmType, &a.Severity, &a.State, &a.ProbableCause, &a.SpecificProblem, &a.RaisedAt, &a.ClearedAt, &a.Details)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find TMF alarm: %w", err)
	}
	return &a, nil
}

func (r *Repository) ListEvents(ctx context.Context, accountID string, limit int) ([]EventRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,source_key,COALESCE(account_id,''),COALESCE(device_id::text,''),COALESCE(service_id::text,''),event_type,event_time,payload FROM tmf_events WHERE ($1='' OR account_id=$1) ORDER BY event_time DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		if err := rows.Scan(&e.ID, &e.SourceKey, &e.AccountID, &e.DeviceID, &e.ServiceID, &e.EventType, &e.EventTime, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repository) ListAlarms(ctx context.Context, accountID, state string, limit int) ([]AlarmRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,source_key,COALESCE(account_id,''),COALESCE(device_id::text,''),COALESCE(service_id::text,''),alarm_type,perceived_severity,state,COALESCE(probable_cause,''),COALESCE(specific_problem,''),raised_at,cleared_at,details FROM tmf_alarms WHERE ($1='' OR account_id=$1) AND ($2='' OR state=$2) ORDER BY raised_at DESC LIMIT $3`, accountID, state, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlarmRecord
	for rows.Next() {
		var a AlarmRecord
		if err := rows.Scan(&a.ID, &a.SourceKey, &a.AccountID, &a.DeviceID, &a.ServiceID, &a.AlarmType, &a.Severity, &a.State, &a.ProbableCause, &a.SpecificProblem, &a.RaisedAt, &a.ClearedAt, &a.Details); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) UpdateAlarmState(ctx context.Context, id, state string) error {
	return r.updateAlarmState(ctx, id, "", state)
}
func (r *Repository) UpdateAlarmStateForAccount(ctx context.Context, id, accountID, state string) error {
	return r.updateAlarmState(ctx, id, accountID, state)
}
func (r *Repository) updateAlarmState(ctx context.Context, id, accountID, state string) error {
	var cleared any
	if state == "cleared" {
		cleared = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE tmf_alarms SET state=$2, cleared_at=$3 WHERE id=$1 AND ($4='' OR account_id=$4)`, id, state, cleared, accountID)
	if err != nil {
		return fmt.Errorf("update TMF alarm: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ClearAlarm closes only a raised alarm belonging to accountID and the
// device/condition pair. Events are retained separately in tmf_events.
func (r *Repository) ClearAlarm(ctx context.Context, accountID, deviceID, sourceKey string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE tmf_alarms SET state='cleared', cleared_at=COALESCE(cleared_at, now()) WHERE source_key=$1 AND account_id=$2 AND device_id=$3::uuid AND state='raised'`, sourceKey, accountID, deviceID)
	if err != nil {
		return fmt.Errorf("clear TMF alarm: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) CreateServiceProblem(ctx context.Context, externalID, accountID, serviceID, problemType, description, priority, alarmID string) (*ServiceProblemRecord, error) {
	return r.CreateServiceProblemRich(ctx, externalID, accountID, serviceID, problemType, description, priority, alarmID, nil, nil, "", "", "")
}

func (r *Repository) CreateServiceProblemRich(ctx context.Context, externalID, accountID, serviceID, problemType, description, priority, alarmID string, eventIDs, resourceIDs []string, impact, severity, rootCause string) (*ServiceProblemRecord, error) {
	id := uuid.New().String()
	var p ServiceProblemRecord
	events, _ := json.Marshal(eventIDs)
	resources, _ := json.Marshal(resourceIDs)
	err := r.db.QueryRowContext(ctx, `INSERT INTO tmf_service_problems (id,external_id,account_id,service_id,problem_type,description,priority,related_alarm_id,related_event_ids,affected_resource_ids,impact,severity,root_cause) VALUES ($1,NULLIF($2,''),$3,NULLIF($4,'')::uuid,$5,$6,NULLIF($7,''),NULLIF($8,'')::uuid,$9,$10,NULLIF($11,''),NULLIF($12,''),NULLIF($13,'')) RETURNING id,COALESCE(external_id,''),account_id,COALESCE(service_id::text,''),status,COALESCE(priority,''),problem_type,description,COALESCE(related_alarm_id::text,''),created_at,resolved_at,COALESCE(resolution,''),related_event_ids,affected_resource_ids,COALESCE(impact,''),COALESCE(severity,''),COALESCE(root_cause,''),resolution_date`, id, externalID, accountID, serviceID, problemType, description, priority, alarmID, events, resources, impact, severity, rootCause).Scan(&p.ID, &p.ExternalID, &p.AccountID, &p.ServiceID, &p.Status, &p.Priority, &p.ProblemType, &p.Description, &p.RelatedAlarmID, &p.CreatedAt, &p.ResolvedAt, &p.Resolution, &p.RelatedEventIDs, &p.AffectedResourceIDs, &p.Impact, &p.Severity, &p.RootCause, &p.ResolutionDate)
	if err != nil {
		return nil, fmt.Errorf("create service problem: %w", err)
	}
	return &p, nil
}

func (r *Repository) FindServiceProblem(ctx context.Context, id string) (*ServiceProblemRecord, error) {
	return r.findServiceProblem(ctx, id, "")
}

func (r *Repository) FindServiceProblemForAccount(ctx context.Context, id, accountID string) (*ServiceProblemRecord, error) {
	return r.findServiceProblem(ctx, id, accountID)
}

func (r *Repository) findServiceProblem(ctx context.Context, id, accountID string) (*ServiceProblemRecord, error) {
	var p ServiceProblemRecord
	err := r.db.QueryRowContext(ctx, `SELECT id,COALESCE(external_id,''),account_id,COALESCE(service_id::text,''),status,COALESCE(priority,''),problem_type,description,COALESCE(related_alarm_id::text,''),created_at,resolved_at,COALESCE(resolution,''),related_event_ids,affected_resource_ids,COALESCE(impact,''),COALESCE(severity,''),COALESCE(root_cause,''),resolution_date FROM tmf_service_problems WHERE (id=$1 OR external_id=$1) AND ($2='' OR account_id=$2)`, id, accountID).Scan(&p.ID, &p.ExternalID, &p.AccountID, &p.ServiceID, &p.Status, &p.Priority, &p.ProblemType, &p.Description, &p.RelatedAlarmID, &p.CreatedAt, &p.ResolvedAt, &p.Resolution, &p.RelatedEventIDs, &p.AffectedResourceIDs, &p.Impact, &p.Severity, &p.RootCause, &p.ResolutionDate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find service problem: %w", err)
	}
	return &p, nil
}

func (r *Repository) UpdateServiceProblemStatus(ctx context.Context, id, accountID, status, resolution string) error {
	var resolved any
	if status == "resolved" || status == "closed" {
		resolved = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE tmf_service_problems SET status=$3,resolution=NULLIF($4,''),resolved_at=$5,resolution_date=$5 WHERE id=$1 AND account_id=$2`, id, accountID, status, resolution, resolved)
	if err != nil {
		return fmt.Errorf("update service problem: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) ListServiceProblems(ctx context.Context, accountID, status string, limit int) ([]ServiceProblemRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,COALESCE(external_id,''),account_id,COALESCE(service_id::text,''),status,COALESCE(priority,''),problem_type,description,COALESCE(related_alarm_id::text,''),created_at,resolved_at,COALESCE(resolution,''),related_event_ids,affected_resource_ids,COALESCE(impact,''),COALESCE(severity,''),COALESCE(root_cause,''),resolution_date FROM tmf_service_problems WHERE ($1='' OR account_id=$1) AND ($2='' OR status=$2) ORDER BY created_at DESC LIMIT $3`, accountID, status, limit)
	if err != nil {
		return nil, fmt.Errorf("list service problems: %w", err)
	}
	defer rows.Close()
	var out []ServiceProblemRecord
	for rows.Next() {
		var p ServiceProblemRecord
		var eventIDs, resourceIDs []byte
		if err := rows.Scan(&p.ID, &p.ExternalID, &p.AccountID, &p.ServiceID, &p.Status, &p.Priority, &p.ProblemType, &p.Description, &p.RelatedAlarmID, &p.CreatedAt, &p.ResolvedAt, &p.Resolution, &eventIDs, &resourceIDs, &p.Impact, &p.Severity, &p.RootCause, &p.ResolutionDate); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(eventIDs, &p.RelatedEventIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(resourceIDs, &p.AffectedResourceIDs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
