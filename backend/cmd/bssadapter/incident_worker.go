package main

import (
	"acs/internal/accounts"
	"context"
	"encoding/json"
	"errors"
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
			h.ingestBulkDataHealth(ctx)
			h.ingestSIMWatch(ctx)
		}
	}
}

func (h *handler) ingestBulkDataHealth(ctx context.Context) {
	recovered, err := h.alertIncidents.RecoverBulkData(ctx)
	if err != nil {
		h.logger.Error("bulk data incident recovery failed", "err", err)
		return
	}
	for i := range recovered {
		h.notifyIncident(ctx, "ALERT_RECOVERED", &recovered[i])
	}
	targets, err := h.alertPolicies.MissingBulkData(ctx, webhookBatchSize)
	if err != nil {
		h.logger.Error("bulk data health evaluation failed", "err", err)
		return
	}
	policies, err := h.alertPolicies.List(ctx)
	if err != nil {
		return
	}
	for _, target := range targets {
		policy, ok := alerting.Resolve(policies, alerting.Target{TenantID: target.TenantID, AccountKey: target.AccountKey, DeviceID: target.DeviceID, CustomerTier: target.CustomerTier, GroupIDs: target.GroupIDs})
		if !ok {
			continue
		}
		next := time.Now().Add(firstEscalationDelay(policy))
		incident, created, err := h.alertIncidents.OpenWithCreated(ctx, target.TenantID, target.DeviceID, "bulk-data:"+target.ReceiverID, alerting.Classify(policy, "bulk_data_missing", false), "Bulk telemetry reports missing or receiver expired", &next, map[string]any{"receiver_id": target.ReceiverID, "last_report_at": target.LastReportAt, "expires_at": target.ExpiresAt, "reporting_interval": target.Interval})
		if err != nil {
			h.logger.Error("bulk data incident creation failed", "err", err)
		} else if created {
			h.notifyIncident(ctx, "ALERT_OPENED", incident)
		}
	}
}

// detectedAtJSON is how a detection time is stored in an incident's details.
func detectedAtJSON(t time.Time) any {
	raw, err := json.Marshal(t)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// ingestSIMWatch turns SIM change verdicts (written by the ACS gateway) into
// incidents: a notice (a different SIM on an allowed network) or an alert (a
// network the plan does not allow). The incident recovers when an operator
// approves the SIM. A device with no customer mapping gets no incident, and
// neither does one with no matching alert policy. Only masked hints and the
// network ever reach an incident.
func (h *handler) ingestSIMWatch(ctx context.Context) {
	recovered, err := h.alertIncidents.RecoverSIMWatch(ctx)
	if err != nil {
		h.logger.Error("failed to recover SIM change incidents", "err", err)
	}
	for i := range recovered {
		h.notifyIncident(ctx, "ALERT_RECOVERED", &recovered[i])
	}
	policies, err := h.alertPolicies.List(ctx)
	if err != nil {
		return
	}
	for offset := 0; ; offset += webhookBatchSize {
		devices, err := h.alertPolicies.SIMWatchDevices(ctx, webhookBatchSize, offset)
		if err != nil {
			h.logger.Error("failed to list SIM change devices for alerting", "err", err)
			return
		}
		h.openSIMWatchIncidents(ctx, policies, devices)
		if len(devices) < webhookBatchSize {
			return
		}
	}
}

func (h *handler) openSIMWatchIncidents(ctx context.Context, policies []alerting.Policy, devices []alerting.SIMWatchDevice) {
	for _, d := range devices {
		p, ok := alerting.Resolve(policies, alerting.Target{TenantID: d.TenantID, AccountKey: d.AccountKey, CustomerTier: d.CustomerTier, GroupIDs: d.GroupIDs, DeviceID: d.DeviceID})
		if !ok {
			continue
		}
		summary := "SIM replaced on this CPE (network " + d.Network + "); approval needed"
		if d.State == "alert" {
			summary = "SIM replaced with a network the plan does not allow (" + d.Network + ")"
		}
		previous, err := h.alertIncidents.ByKey(ctx, d.TenantID, d.DeviceID, alerting.SIMChangeConditionKey)
		if err != nil && !errors.Is(err, alerting.ErrIncidentNotFound) {
			h.logger.Error("failed to look up SIM change incident", "err", err, "device_id", d.DeviceID)
			continue
		}
		next := time.Now().UTC().Add(firstEscalationDelay(p))
		incident, created, err := h.alertIncidents.OpenWithCreated(ctx, d.TenantID, d.DeviceID, alerting.SIMChangeConditionKey, alerting.Classify(p, "sim_change_"+d.State, false), summary, &next, map[string]any{
			"state": d.State, "network": d.Network, "iccid_hint": d.ICCIDHint, "imsi_hint": d.IMSIHint,
			"previous_iccid_hint": d.PreviousICCIDHint, "previous_imsi_hint": d.PreviousIMSIHint, "detected_at": d.DetectedAt,
		})
		if err != nil {
			h.logger.Error("failed to open SIM change incident", "err", err, "device_id", d.DeviceID)
			continue
		}
		reopened := previous != nil && (previous.State == alerting.IncidentRecovered || previous.State == alerting.IncidentClosed)
		// An incident that is already open is told about again when the
		// condition got worse (a higher priority) or is a different SIM
		// (another detection time); otherwise a swap onto a network the plan
		// does not allow would sit silently behind an unattended notice.
		worse := previous != nil && !reopened && incident.Priority < previous.Priority
		differs := previous != nil && !reopened && previous.Details["detected_at"] != detectedAtJSON(d.DetectedAt)
		if worse {
			if err := h.alertIncidents.RescheduleEscalation(ctx, incident.ID, &next); err != nil {
				h.logger.Error("failed to restart SIM change escalation", "err", err, "device_id", d.DeviceID)
			}
		}
		if created || reopened || worse || differs {
			h.notifyIncident(ctx, "ALERT_OPENED", incident)
		}
	}
}

func (h *handler) ingestOfflineDevices(ctx context.Context) {
	if recovered, err := h.alertIncidents.RecoverOnlineIncidents(ctx); err != nil {
		h.logger.Error("failed to recover online CPE incidents", "err", err)
	} else if len(recovered) > 0 {
		h.logger.Info("recovered online CPE incidents", "count", len(recovered))
		for i := range recovered {
			h.notifyIncident(ctx, "ALERT_RECOVERED", &recovered[i])
		}
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
		p, ok := alerting.Resolve(policies, alerting.Target{TenantID: d.TenantID, AccountKey: d.AccountKey, CustomerTier: d.CustomerTier, GroupIDs: d.GroupIDs, DeviceID: d.DeviceID})
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
		tier, accountKey, tierErr := h.alertPolicies.Mapping(ctx, i.TenantID, i.DeviceID)
		if tierErr != nil {
			continue
		}
		p, ok := alerting.Resolve(policies, alerting.Target{TenantID: i.TenantID, AccountKey: accountKey, DeviceID: i.DeviceID, GroupIDs: groupIDs, CustomerTier: tier})
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
		subs, err := h.webhooks.MatchingSubscriptions(accounts.ForDevice(ctx, i.DeviceID), i.TenantID, "ALERT_ESCALATED")
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
		tier, accountKey, err := h.alertPolicies.Mapping(ctx, event.AccountID, event.DeviceID)
		if err != nil {
			h.logger.Error("failed to resolve customer tier", "err", err, "device_id", event.DeviceID)
			continue
		}
		p, ok := alerting.Resolve(policies, alerting.Target{TenantID: event.AccountID, AccountKey: accountKey, CustomerTier: tier, GroupIDs: groupIDs, DeviceID: event.DeviceID})
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
	subs, err := h.webhooks.MatchingSubscriptions(accounts.ForDevice(ctx, incident.DeviceID), incident.TenantID, eventType)
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
