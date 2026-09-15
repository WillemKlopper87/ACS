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

func (h *handler) runOfflineIncidentLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.ingestOfflineDevices(ctx)
		}
	}
}

func (h *handler) ingestOfflineDevices(ctx context.Context) {
	if n, err := h.alertIncidents.RecoverOnline(ctx); err != nil {
		h.logger.Error("failed to recover online CPE incidents", "err", err)
	} else if n > 0 {
		h.logger.Info("recovered online CPE incidents", "count", n)
	}
	devices, err := h.alertPolicies.OfflineDevices(ctx, webhookBatchSize)
	if err != nil {
		h.logger.Error("failed to list offline devices for alerting", "err", err)
		return
	}
	policies, err := h.alertPolicies.List(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, d := range devices {
		p, ok := alerting.Resolve(policies, alerting.Target{TenantID: d.TenantID, CustomerTier: d.CustomerTier, GroupIDs: d.GroupIDs, DeviceID: d.DeviceID})
		if !ok || p.OfflineAfter <= 0 {
			continue
		}
		if d.LastInformAt != nil && now.Sub(*d.LastInformAt) < p.OfflineAfter {
			continue
		}
		priority := alerting.Classify(p, "offline", true)
		next := now.Add(firstEscalationDelay(p))
		incident, created, err := h.alertIncidents.OpenWithCreated(ctx, d.TenantID, d.DeviceID, "acs-offline", priority, "CPE has not checked in", &next, map[string]any{"last_inform_at": d.LastInformAt, "trigger": "liveness"})
		if err != nil {
			h.logger.Error("failed to open offline CPE incident", "err", err, "device_id", d.DeviceID)
		} else if created {
			h.notifyIncident(ctx, "ALERT_OPENED", incident)
		}
	}
}

func (h *handler) runEscalationLoop(ctx context.Context) {
	ticker := time.NewTicker(webhookNotifyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.processEscalations(ctx)
		}
	}
}

func (h *handler) processEscalations(ctx context.Context) {
	incidents, err := h.alertIncidents.Due(ctx, webhookBatchSize)
	if err != nil {
		h.logger.Error("failed to list due alert incidents", "err", err)
		return
	}
	policies, err := h.alertPolicies.List(ctx)
	if err != nil {
		return
	}
	for _, i := range incidents {
		groupIDs, groupErr := h.alertPolicies.GroupIDsForDevice(ctx, i.DeviceID)
		if groupErr != nil {
			continue
		}
		tier, tierErr := h.alertPolicies.CustomerTier(ctx, i.TenantID, i.DeviceID)
		if tierErr != nil {
			continue
		}
		p, ok := alerting.Resolve(policies, alerting.Target{TenantID: i.TenantID, DeviceID: i.DeviceID, GroupIDs: groupIDs, CustomerTier: tier})
		if !ok {
			continue
		}
		if i.EscalationStage >= len(p.Steps) {
			if err := h.alertIncidents.Advance(ctx, i.ID, nil); err != nil {
				h.logger.Warn("failed to close escalation clock", "err", err)
			}
			continue
		}
		step := p.Steps[i.EscalationStage]
		subs, err := h.webhooks.MatchingSubscriptions(ctx, i.TenantID, "ALERT_ESCALATED")
		if err == nil {
			payload := map[string]any{"event_type": "ALERT_ESCALATED", "incident_id": i.ID, "tenant_id": i.TenantID, "device_id": i.DeviceID, "priority": i.Priority, "summary": i.Summary, "stage": i.EscalationStage, "destination": step.Destination, "recipient": step.Recipient, "escalated_at": time.Now().UTC()}
			for _, s := range subs {
				_ = h.webhooks.EnqueueDelivery(ctx, s.ID, "ALERT_ESCALATED", payload)
			}
		}
		var next *time.Time
		if i.EscalationStage+1 < len(p.Steps) {
			t := time.Now().UTC().Add(p.Steps[i.EscalationStage+1].After)
			next = &t
		}
		_ = h.alertIncidents.Advance(ctx, i.ID, next)
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
		groupIDs, err := h.alertPolicies.GroupIDsForDevice(ctx, event.DeviceID)
		if err != nil {
			h.logger.Error("failed to resolve CPE alert groups", "err", err, "device_id", event.DeviceID)
			continue
		}
		tier, err := h.alertPolicies.CustomerTier(ctx, event.AccountID, event.DeviceID)
		if err != nil {
			h.logger.Error("failed to resolve customer tier", "err", err, "device_id", event.DeviceID)
			continue
		}
		p, ok := alerting.Resolve(policies, alerting.Target{TenantID: event.AccountID, CustomerTier: tier, GroupIDs: groupIDs, DeviceID: event.DeviceID})
		if !ok {
			continue
		}
		priority := alerting.Classify(p, body.FaultCode, false)
		next := event.EventTime.Add(firstEscalationDelay(p))
		incident, created, err := h.alertIncidents.OpenWithCreated(ctx, event.AccountID, event.DeviceID, telemetry.ConditionKey(body.Protocol, event.DeviceID, body.FaultCode), priority, body.Message, &next, map[string]any{"event_id": event.ID, "fault_code": body.FaultCode, "protocol": body.Protocol, "job_id": body.JobID})
		if err != nil {
			h.logger.Error("failed to open CPE incident", "err", err, "event_id", event.ID)
		} else if created {
			h.notifyIncident(ctx, "ALERT_OPENED", incident)
		}
	}
}

func (h *handler) notifyIncident(ctx context.Context, eventType string, incident *alerting.Incident) {
	if incident == nil {
		return
	}
	subs, err := h.webhooks.MatchingSubscriptions(ctx, incident.TenantID, eventType)
	if err != nil {
		h.logger.Error("failed to match incident notifications", "err", err)
		return
	}
	payload := map[string]any{"event_type": eventType, "incident_id": incident.ID, "tenant_id": incident.TenantID, "device_id": incident.DeviceID, "priority": incident.Priority, "summary": incident.Summary, "state": incident.State, "opened_at": incident.FirstSeenAt}
	for _, s := range subs {
		if err := h.webhooks.EnqueueDelivery(ctx, s.ID, eventType, payload); err != nil {
			h.logger.Error("failed to enqueue incident notification", "err", err, "subscription_id", s.ID)
		}
	}
}

func firstEscalationDelay(p alerting.Policy) time.Duration {
	if len(p.Steps) > 0 && p.Steps[0].After > 0 {
		return p.Steps[0].After
	}
	return 15 * time.Minute
}
