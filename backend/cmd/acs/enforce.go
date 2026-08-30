// Policy enforcement (build plan §4 Phase 7 / design doc v3 Phase 7:
// "Policy engine"). Checked on every Inform — this is what makes it a
// continuous compliance mechanism rather than a one-time push like
// firmware rollouts or a timer like scheduled jobs: any device that
// drifts (a config reset, a factory-default reflash, manual tampering)
// gets corrected the next time it checks in, indefinitely, with no
// operator action required.
package main

import (
	"context"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
)

// enforcePolicies checks this Inform's reported parameters against every
// enabled policy matching the device's manufacturer/product_class, and
// queues a correcting SET_PARAMETER for any that drifted. Deliberately
// only acts on parameters this Inform actually reported — a policy for a
// parameter the CPE didn't include this time is neither confirmed
// compliant nor confirmed drifted, so nothing is queued for it (the next
// Inform that does report it gets checked then). No explicit
// dedup-against-in-flight-jobs check: periodic Informs are minutes apart
// and a queued SET_PARAMETER plus its auto-confirm GET_PARAMETER (Phase 2)
// normally resolves within one session, so by the next Inform the cache
// already reflects the corrected value and nothing re-fires — a real
// tradeoff, not an oversight, documented here rather than silently relied on.
func (h *handler) enforcePolicies(ctx context.Context, device *devices.Device, reported []cwmp.ParameterValueStruct) {
	if len(reported) == 0 {
		return
	}

	policies, err := h.policies.ForDevice(ctx, device.Manufacturer, device.ProductClass)
	if err != nil {
		h.logger.Error("failed to load policies for device", "err", err, "device_id", device.ID)
		return
	}
	if len(policies) == 0 {
		return
	}

	reportedByName := make(map[string]string, len(reported))
	for _, p := range reported {
		reportedByName[p.Name] = p.Value
	}

	for _, pol := range policies {
		actual, reportedThisInform := reportedByName[pol.ParameterName]
		if !reportedThisInform || actual == pol.DesiredValue {
			continue
		}

		job, err := h.jobs.Create(ctx, device.ID, jobs.TypeSetParameter, jobs.SetParameterPayload{
			Parameters: []jobs.ParameterWrite{{Name: pol.ParameterName, Value: pol.DesiredValue, Type: "xsd:string"}},
		}, "policy:"+pol.Name)
		if err != nil {
			h.logger.Error("failed to queue policy enforcement job", "err", err, "policy_id", pol.ID, "device_id", device.ID)
			continue
		}

		if err := h.auditor.Record(ctx, "policy:"+pol.Name, device.ID, "PolicyEnforced", map[string]any{
			"policy_id": pol.ID, "parameter": pol.ParameterName, "reported": actual,
			"desired": pol.DesiredValue, "command_key": job.CommandKey,
		}); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
		h.logger.Info("policy drift corrected", "policy_id", pol.ID, "policy_name", pol.Name,
			"device_id", device.ID, "parameter", pol.ParameterName, "reported", actual, "desired", pol.DesiredValue,
			"command_key", job.CommandKey)
	}
}

// correlateValueChangeEvents consumes the CommandKey TR-069 attaches to a
// "4 VALUE CHANGE" Inform event (Annex A EventStruct.CommandKey): when a
// parameter under active notification (SetParameterAttributes) changes,
// the CPE SHOULD echo back the ParameterKey of the SetParameterValues that
// caused it, letting the ACS confirm out-of-band that its own write
// actually took effect on the device — distinct from the ordinary
// GetParameterValues auto-confirm, which only proves the value stuck at
// read time, not that the CPE itself flagged the change. Until now this
// was parsed (cwmp.EventStruct.CommandKey) but never read anywhere.
//
// An empty CommandKey means the value changed on its own (local UI,
// factory reset, another management system, not this ACS) — worth its own
// audit trail entry, since it's evidence of drift a policy hasn't caught
// up to yet, not something to correlate to a job.
func (h *handler) correlateValueChangeEvents(ctx context.Context, device *devices.Device, events []cwmp.EventStruct, reported []cwmp.ParameterValueStruct) {
	for _, e := range events {
		if len(e.EventCode) == 0 || e.EventCode[0:1] != "4" {
			continue
		}

		details := map[string]any{"parameters": reported}

		if e.CommandKey == "" {
			if err := h.auditor.Record(ctx, "system", device.ID, "UnsolicitedValueChange", details); err != nil {
				h.logger.Error("failed to write audit record", "err", err)
			}
			continue
		}

		job, err := h.jobs.ByCommandKey(ctx, e.CommandKey)
		if err != nil {
			h.logger.Warn("VALUE CHANGE event for unrecognized command_key", "command_key", e.CommandKey, "device_id", device.ID, "err", err)
			continue
		}

		details["job_id"] = job.ID
		details["command_key"] = job.CommandKey
		if err := h.auditor.Record(ctx, "system", device.ID, "ValueChangeConfirmed", details); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
		h.logger.Info("VALUE CHANGE confirmed by CommandKey", "job_id", job.ID, "command_key", job.CommandKey, "device_id", device.ID)
	}
}
