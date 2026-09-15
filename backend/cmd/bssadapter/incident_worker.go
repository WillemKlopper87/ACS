package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"acs/internal/alerting"
	"acs/internal/tmf/telemetry"
)

func (h *handler) runIncidentIngestLoop(ctx context.Context) {
	ticker := time.NewTicker(webhookNotifyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.ingestTMFFaults(ctx)
		}
	}
}

func (h *handler) ingestTMFFaults(ctx context.Context) {
	events, err := h.mappings.ListEvents(ctx, "", webhookBatchSize)
	if err != nil {
		h.logger.Error("failed to list TMF events for incident ingestion", "err", err)
		return
	}
	policies, err := h.alertPolicies.List(ctx)
	if err != nil {
		h.logger.Error("failed to list alert policies", "err", err)
		return
	}
	for _, event := range events {
		if event.EventType != "DeviceFault" || strings.TrimSpace(event.AccountID) == "" || strings.TrimSpace(event.DeviceID) == "" {
			continue
		}
		var body struct{ Protocol, JobID, FaultCode, Message string }
		if json.Unmarshal(event.Payload, &body) != nil || body.Message == "" {
			continue
		}
		p, ok := alerting.Resolve(policies, alerting.Target{TenantID: event.AccountID, DeviceID: event.DeviceID})
		if !ok {
			continue
		}
		priority := alerting.Classify(p, body.FaultCode, false)
		next := event.EventTime.Add(firstEscalationDelay(p))
		_, err := h.alertIncidents.Open(ctx, event.AccountID, event.DeviceID, telemetry.ConditionKey(body.Protocol, event.DeviceID, body.FaultCode), priority, body.Message, &next, map[string]any{"event_id": event.ID, "fault_code": body.FaultCode, "protocol": body.Protocol, "job_id": body.JobID})
		if err != nil {
			h.logger.Error("failed to open CPE incident", "err", err, "event_id", event.ID)
		}
	}
}

func firstEscalationDelay(p alerting.Policy) time.Duration {
	if len(p.Steps) > 0 && p.Steps[0].After > 0 {
		return p.Steps[0].After
	}
	return 15 * time.Minute
}
