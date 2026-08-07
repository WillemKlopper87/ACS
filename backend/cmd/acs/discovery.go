// Parameter discovery (nice-to-have backlog): a full-tree
// GetParameterNames(root, NextLevel=false) sweep queued automatically the
// moment a device's first BOOTSTRAP Inform arrives, so the console shows
// what a CPE actually supports instead of relying solely on a static
// vendor parameter registry — and so devices.DataModelRoot gets set to a
// real value instead of sitting at UNKNOWN forever (see
// devices.Repository.UpsertFromInform's doc comment). Also exposed as an
// on-demand REST action (cmd/api) for devices that connected before this
// feature existed, or after a firmware upgrade changes what's supported.
//
// TR-181 (Device:2) is tried first — the project's existing convention
// (build plan §3) is that new-model devices default to it — with one
// automatic fallback to TR-098 (InternetGatewayDevice:1) if the first
// attempt comes back faulted or empty, mirroring cmd/probe's step order
// (internal/cwmp/session.go). The fallback is a second, independent job
// rather than a retry of the first, so both attempts show up plainly in
// the Jobs screen.
package main

import (
	"context"
	"encoding/json"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
)

const (
	rootDevice2 = "Device."
	rootIGD1    = "InternetGatewayDevice."
)

func (h *handler) autoDiscoverParameters(ctx context.Context, device *devices.Device) {
	root, fallback := rootDevice2, rootIGD1
	if device.DataModelRoot == devices.DataModelRootIGD1 {
		root, fallback = rootIGD1, rootDevice2
	}

	job, err := h.jobs.Create(ctx, device.ID, jobs.TypeParameterDiscovery,
		jobs.ParameterDiscoveryPayload{Root: root, FallbackRoot: fallback}, "system:bootstrap-discovery")
	if err != nil {
		h.logger.Error("failed to queue parameter discovery", "err", err, "device_id", device.ID)
		return
	}
	h.logger.Info("parameter discovery queued on BOOTSTRAP", "device_id", device.ID, "root", root, "command_key", job.CommandKey)
}

// discoveryResultDetail is what PARAMETER_DISCOVERY's SUCCESS jobs carry in
// result_detail (jobs.MarkSuccessWithDetail) — there's no confirmation read
// to fold this into (unlike SET_PARAMETER's), so it has nowhere else to
// live.
type discoveryResultDetail struct {
	Root           string `json:"root"`
	ParameterCount int    `json:"parameter_count"`
	WritableCount  int    `json:"writable_count"`
}

// completeParameterDiscovery handles PARAMETER_DISCOVERY's response
// specially (see the call site in completeJob) because a fault or an empty
// name list both mean the same thing here — "this device doesn't have
// anything under that root" — and should trigger one automatic fallback
// attempt at the other root rather than being treated as an ordinary job
// failure.
func (h *handler) completeParameterDiscovery(ctx context.Context, deviceID string, job *jobs.Job, body cwmp.InboundBody) {
	var payload jobs.ParameterDiscoveryPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		h.logger.Error("failed to unmarshal parameter discovery payload", "err", err, "job_id", job.ID)
		return
	}

	empty := body.Fault != nil || body.GetParameterNamesResponse == nil || len(body.GetParameterNamesResponse.ParameterList) == 0

	if empty {
		detail := "device returned no parameters under " + payload.Root
		if body.Fault != nil {
			detail = body.Fault.CWMPMessage()
		}
		if err := h.jobs.MarkFailed(ctx, job.ID, "", detail); err != nil {
			h.logger.Error("failed to mark discovery job failed", "err", err, "job_id", job.ID)
		}

		if payload.IsFallback || payload.FallbackRoot == "" {
			h.logger.Warn("parameter discovery found no parameters under either root", "device_id", deviceID, "root", payload.Root)
			return
		}

		fallback, err := h.jobs.Create(ctx, deviceID, jobs.TypeParameterDiscovery,
			jobs.ParameterDiscoveryPayload{Root: payload.FallbackRoot, IsFallback: true}, "system:bootstrap-discovery")
		if err != nil {
			h.logger.Error("failed to queue parameter discovery fallback", "err", err, "device_id", deviceID)
			return
		}
		h.logger.Info("parameter discovery falling back to other root", "device_id", deviceID,
			"tried_root", payload.Root, "fallback_root", payload.FallbackRoot, "command_key", fallback.CommandKey)
		return
	}

	list := body.GetParameterNamesResponse.ParameterList
	writableByName := make(map[string]bool, len(list))
	writableCount := 0
	for _, p := range list {
		writable := p.Writable == "1"
		writableByName[p.Name] = writable
		if writable {
			writableCount++
		}
	}

	if err := h.params.SaveNames(ctx, deviceID, writableByName); err != nil {
		h.logger.Error("failed to save discovered parameter names", "err", err, "device_id", deviceID)
	}

	root := devices.DataModelRootDevice2
	if payload.Root == rootIGD1 {
		root = devices.DataModelRootIGD1
	}
	if err := h.devices.UpdateDataModelRoot(ctx, deviceID, root); err != nil {
		h.logger.Error("failed to update data model root", "err", err, "device_id", deviceID)
	}

	if err := h.jobs.MarkSuccessWithDetail(ctx, job.ID, discoveryResultDetail{
		Root: payload.Root, ParameterCount: len(list), WritableCount: writableCount,
	}); err != nil {
		h.logger.Error("failed to mark discovery job success", "err", err, "job_id", job.ID)
	}
	h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()

	if err := h.auditor.Record(ctx, "system", deviceID, "ParameterDiscoveryCompleted", map[string]any{
		"job_id": job.ID, "command_key": job.CommandKey, "root": payload.Root,
		"parameter_count": len(list), "writable_count": writableCount,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("parameter discovery completed", "device_id", deviceID, "root", payload.Root,
		"parameter_count", len(list), "writable_count", writableCount, "command_key", job.CommandKey)
}
