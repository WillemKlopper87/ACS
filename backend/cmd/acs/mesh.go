// Mesh satellite -> gateway linking (field test 2026-10-30). See
// internal/devices/gateway_link.go for the model. This file wires it into the
// CWMP session: read GatewayInfo once the tree is known (and again after
// every boot, when a satellite may have rejoined a different gateway), and
// re-resolve the link whenever those values reach the parameter cache.
package main

import (
	"context"
	"strings"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
)

const gatewayInfoJobCreator = "system:gateway-info"

// queueGatewayInfoRead queues a targeted GetParameterValues of the TR-111
// GatewayInfo leaves when the discovered tree has them.
func (h *handler) queueGatewayInfoRead(ctx context.Context, deviceID string, names map[string]bool) {
	paths := devices.GatewayInfoReadPaths(names)
	if len(paths) == 0 {
		return
	}
	job, err := h.jobs.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, gatewayInfoJobCreator)
	if err != nil {
		h.logger.Error("failed to queue gateway info read", "err", err, "device_id", deviceID)
		return
	}
	h.metrics.JobsCreatedTotal.WithLabelValues(jobs.TypeGetParameter).Inc()
	h.logger.Info("queued gateway info read", "device_id", deviceID, "command_key", job.CommandKey)
}

// refreshGatewayInfoOnBoot re-reads GatewayInfo after a "1 BOOT" Inform on a
// device whose discovered tree has it, so a satellite moved to another
// gateway (or re-paired) is re-linked without waiting for a new discovery.
func (h *handler) refreshGatewayInfoOnBoot(ctx context.Context, deviceID string) {
	names, err := h.params.GetNames(ctx, deviceID)
	if err != nil {
		h.logger.Error("load discovered names for gateway info", "err", err, "device_id", deviceID)
		return
	}
	if names == nil {
		return
	}
	h.queueGatewayInfoRead(ctx, deviceID, names.Names)
}

func hasGatewayInfo(list []cwmp.ParameterValueStruct) bool {
	for _, p := range list {
		if strings.HasPrefix(p.Name, devices.GatewayInfoPrefix) {
			return true
		}
	}
	return false
}

// refreshGatewayLink re-resolves a device's gateway from the cached
// GatewayInfo values. Called after any cache write carrying GatewayInfo, so
// the link follows the values wherever they came from (targeted read,
// operator live GET, Inform parameter list).
func (h *handler) refreshGatewayLink(ctx context.Context, deviceID string) {
	cached, err := h.params.Get(ctx, deviceID)
	if err != nil {
		h.logger.Error("load gateway info from cache", "err", err, "device_id", deviceID)
		return
	}
	values := make(map[string]string, len(devices.GatewayInfoPaths))
	for _, path := range devices.GatewayInfoPaths {
		values[path] = cached[path].Value
	}
	gateway, ok := devices.GatewayIdentityFromValues(values)
	link, err := h.devices.LinkGateway(ctx, deviceID, gateway, ok)
	if err != nil {
		h.logger.Error("failed to update gateway link", "err", err, "device_id", deviceID)
		return
	}
	if !link.Changed {
		return
	}
	gatewayDeviceID := ""
	if link.GatewayDeviceID != nil {
		gatewayDeviceID = *link.GatewayDeviceID
	}
	h.logger.Info("mesh gateway link updated", "device_id", deviceID,
		"gateway_oui_serial", link.GatewayOUISerial, "gateway_device_id", gatewayDeviceID)
	if h.auditor == nil {
		return
	}
	if err := h.auditor.Record(ctx, "system", deviceID, "GatewayLinkUpdated", map[string]any{
		"gateway_oui_serial": link.GatewayOUISerial, "gateway_device_id": gatewayDeviceID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
}

// adoptMeshSatellites links satellites that named this device as their
// gateway before it was registered.
func (h *handler) adoptMeshSatellites(ctx context.Context, device *devices.Device) {
	n, err := h.devices.AdoptSatellites(ctx, device)
	if err != nil {
		h.logger.Error("failed to adopt mesh satellites", "err", err, "device_id", device.ID)
		return
	}
	if n > 0 {
		h.logger.Info("linked waiting mesh satellites to gateway", "device_id", device.ID, "satellites", n)
	}
}

// informHasEvent matches a full event code ("1 BOOT"), unlike
// cwmp.Inform.HasEventCode's first-character match, which would also accept
// "10 AUTONOMOUS TRANSFER COMPLETE" for "1".
func informHasEvent(inform *cwmp.Inform, code string) bool {
	for _, e := range inform.Event {
		if strings.EqualFold(strings.TrimSpace(e.EventCode), code) {
			return true
		}
	}
	return false
}
