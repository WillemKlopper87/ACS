// Zero-touch auto-provisioning (nice-to-have feature backlog): the other
// half of internal/templates — applying a matching template's full
// parameter set automatically the moment a device's first BOOTSTRAP
// Inform arrives, no operator action required. Unlike enforce.go's
// continuous policy checking (every Inform, forever), this fires exactly
// once per device's lifetime — BOOTSTRAP means "this device is
// registering with this ACS for the first time" (TR-069 §3.7.1.5), not
// "this device just rebooted" (that's event code "1 BOOT").
package main

import (
	"context"

	"acs/internal/devices"
	"acs/internal/jobs"
)

func (h *handler) applyAutoProvisioningTemplates(ctx context.Context, device *devices.Device) {
	matches, err := h.templates.MatchingAutoApply(ctx, device.Manufacturer, device.ProductClass)
	if err != nil {
		h.logger.Error("failed to load auto-apply templates for device", "err", err, "device_id", device.ID)
		return
	}

	for _, t := range matches {
		params := make([]jobs.ParameterWrite, len(t.Parameters))
		for i, p := range t.Parameters {
			params[i] = jobs.ParameterWrite{Name: p.Name, Value: p.Value, Type: p.Type}
		}

		job, err := h.jobs.Create(ctx, device.ID, jobs.TypeSetParameter,
			jobs.SetParameterPayload{Parameters: params}, "template:"+t.Name)
		if err != nil {
			h.logger.Error("failed to queue auto-provisioning template", "err", err, "template_id", t.ID, "device_id", device.ID)
			continue
		}

		if err := h.auditor.Record(ctx, "template:"+t.Name, device.ID, "ConfigTemplateAutoApplied", map[string]any{
			"template_id": t.ID, "name": t.Name, "command_key": job.CommandKey, "parameter_count": len(params),
		}); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
		h.logger.Info("auto-provisioning template applied on BOOTSTRAP", "template_id", t.ID, "name", t.Name,
			"device_id", device.ID, "command_key", job.CommandKey)
	}
}
