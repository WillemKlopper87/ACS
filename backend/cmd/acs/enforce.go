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
