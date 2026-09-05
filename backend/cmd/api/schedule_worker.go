package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/jobs"
	"acs/internal/observability"
	"acs/internal/scheduler"
)

// schedulePollInterval trades off promptness against load — a scheduled
// job's own interval_seconds is never shorter than a minute (scheduler.
// MinIntervalSeconds), so checking every 10s is frequent enough that a
// due job never waits long without polling the table on every request.
const schedulePollInterval = 10 * time.Second

// scheduleWorker is the background loop that turns due scheduled_jobs
// rows into real jobs.Repository.Create calls — the same
// "poll for due work" shape connectionRequestWorker already uses for
// CONNECTION_REQUEST, generalized to any job type a schedule names.
type scheduleWorker struct {
	logger    *slog.Logger
	schedules *scheduler.Repository
	jobs      *jobs.Repository
	devices   *devices.Repository
	groups    *devices.GroupRepository
	auditor   *observability.Auditor
}

func (w *scheduleWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(schedulePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sj, err := w.schedules.LeaseDue(ctx)
			if err != nil {
				w.logger.Error("failed to lease due scheduled job", "err", err)
				continue
			}
			if sj == nil {
				continue
			}
			go w.fire(ctx, sj)
		}
	}
}

// fire resolves a scheduled job's target to one or more device IDs and
// creates one real job per device — the same fan-out bulkAction already
// does for an operator-initiated bulk action, just triggered by a timer
// instead of an HTTP request.
func (w *scheduleWorker) fire(ctx context.Context, sj *scheduler.ScheduledJob) {
	var candidateIDs []string
	switch sj.TargetType {
	case scheduler.TargetDevice:
		candidateIDs = []string{sj.TargetID}
	case scheduler.TargetGroup:
		ids, err := w.groups.MemberDeviceIDs(ctx, sj.TargetID)
		if err != nil {
			w.logger.Error("failed to resolve scheduled job group target", "err", err, "scheduled_job_id", sj.ID, "group_id", sj.TargetID)
			return
		}
		candidateIDs = ids
	default:
		w.logger.Error("scheduled job has unknown target_type", "scheduled_job_id", sj.ID, "target_type", sj.TargetType)
		return
	}

	// audit P0.6: re-check authorization at fire time, not just at
	// creation — a device (or a group member) can move to a different
	// customer after the schedule was created, and a stale schedule
	// created before this remediation may still carry no customer_id at
	// all. Every candidate must currently belong to the schedule's own
	// customer (platform-global schedules, customer_id nil, skip this
	// check by design).
	deviceIDs := candidateIDs
	if sj.CustomerID != nil {
		deviceIDs = deviceIDs[:0]
		for _, id := range candidateIDs {
			d, err := w.devices.Get(ctx, id)
			if err != nil {
				w.logger.Error("failed to resolve scheduled job target device", "err", err, "scheduled_job_id", sj.ID, "device_id", id)
				continue
			}
			if d.CustomerID == nil || *d.CustomerID != *sj.CustomerID {
				w.logger.Warn("scheduled job target no longer in the schedule's customer, skipping", "scheduled_job_id", sj.ID, "device_id", id)
				continue
			}
			deviceIDs = append(deviceIDs, id)
		}
	}

	var payload any
	// The cases below must stay in step with
	// scheduler.SchedulableJobTypes, which is what createScheduledJob
	// validates against — TestSchedulableJobTypesMatchWorker asserts they
	// agree, so adding one place without the other fails the build's
	// tests rather than silently accepting a schedule that never fires.
	switch sj.JobType {
	case jobs.TypeSetParameter:
		var p jobs.SetParameterPayload
		if err := json.Unmarshal(sj.Payload, &p); err != nil {
			w.logger.Error("failed to unmarshal scheduled job payload", "err", err, "scheduled_job_id", sj.ID)
			return
		}
		payload = p
	case jobs.TypeGetParameter:
		var p jobs.GetParameterPayload
		if err := json.Unmarshal(sj.Payload, &p); err != nil {
			w.logger.Error("failed to unmarshal scheduled job payload", "err", err, "scheduled_job_id", sj.ID)
			return
		}
		payload = p
	case jobs.TypeDiagnosticsPing:
		var p jobs.DiagnosticsPingPayload
		if err := json.Unmarshal(sj.Payload, &p); err != nil {
			w.logger.Error("failed to unmarshal scheduled job payload", "err", err, "scheduled_job_id", sj.ID)
			return
		}
		payload = p
	case jobs.TypeConnectionRequest:
		var p jobs.ConnectionRequestPayload
		if err := json.Unmarshal(sj.Payload, &p); err != nil {
			w.logger.Error("failed to unmarshal scheduled job payload", "err", err, "scheduled_job_id", sj.ID)
			return
		}
		payload = p
	default:
		w.logger.Error("scheduled job has unknown job_type", "scheduled_job_id", sj.ID, "job_type", sj.JobType)
		return
	}

	created := 0
	for _, deviceID := range deviceIDs {
		jobPayload := payload
		// A group target can span devices with different discovered
		// data_model_root values — the shared payload parsed above can't
		// carry one Prefix correct for all of them, so it's re-resolved
		// per device here rather than once for the whole fan-out (same
		// reason cmd/api's createDiagnosticsPing resolves it per device
		// at creation time).
		if sj.JobType == jobs.TypeDiagnosticsPing {
			p := payload.(jobs.DiagnosticsPingPayload)
			device, err := w.devices.Get(ctx, deviceID)
			if err != nil {
				w.logger.Error("failed to resolve device for scheduled diagnostics ping", "err", err, "scheduled_job_id", sj.ID, "device_id", deviceID)
				continue
			}
			p.Prefix = adapters.DiagnosticsPrefix(device.DataModelRoot, adapters.DiagnosticPing)
			jobPayload = p
		}
		if _, err := w.jobs.Create(ctx, deviceID, sj.JobType, jobPayload, "scheduler:"+sj.Name); err != nil {
			w.logger.Error("failed to create job from schedule", "err", err, "scheduled_job_id", sj.ID, "device_id", deviceID)
			continue
		}
		created++
	}

	if err := w.auditor.Record(ctx, "scheduler:"+sj.Name, "", "ScheduledJobFired", map[string]any{
		"scheduled_job_id": sj.ID, "job_type": sj.JobType, "target_type": sj.TargetType,
		"target_id": sj.TargetID, "devices_targeted": len(deviceIDs), "jobs_created": created,
	}); err != nil {
		w.logger.Error("failed to write audit record", "err", err)
	}
	w.logger.Info("scheduled job fired", "scheduled_job_id", sj.ID, "name", sj.Name,
		"devices_targeted", len(deviceIDs), "jobs_created", created)
}
