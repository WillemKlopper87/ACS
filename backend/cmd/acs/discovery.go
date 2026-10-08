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
	"errors"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/parameters"
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

	firmware := ""
	if values, err := h.params.Get(ctx, device.ID); err == nil {
		firmware = values["Device.DeviceInfo.SoftwareVersion"].Value
		if firmware == "" {
			firmware = values["InternetGatewayDevice.DeviceInfo.SoftwareVersion"].Value
		}
	}
	job, err := h.jobs.CreateWithMaxAttempts(ctx, device.ID, jobs.TypeParameterDiscovery,
		jobs.ParameterDiscoveryPayload{Root: root, FallbackRoot: fallback, Incremental: true, Firmware: firmware}, "system:bootstrap-discovery", jobs.DiscoveryMaxObjects)
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

	empty := body.Fault != nil || body.GetParameterNamesResponse == nil || (len(body.GetParameterNamesResponse.ParameterList) == 0 && payload.Cursor == "")

	if empty {
		detail := "device returned no parameters under " + payload.Root
		if body.Fault != nil {
			detail = body.Fault.CWMPMessage()
		}
		if err := h.jobs.MarkFailed(ctx, job.ID, "", detail); err != nil {
			h.logger.Error("failed to mark discovery job failed", "err", err, "job_id", job.ID)
		}

		if payload.IsFallback || payload.FallbackRoot == "" || payload.Cursor != "" {
			h.logger.Warn("parameter discovery found no parameters under either root", "device_id", deviceID, "root", payload.Root)
			return
		}

		fallback, err := h.jobs.CreateWithMaxAttempts(ctx, deviceID, jobs.TypeParameterDiscovery,
			jobs.ParameterDiscoveryPayload{Root: payload.FallbackRoot, IsFallback: true, Incremental: payload.Incremental, Firmware: payload.Firmware}, "system:bootstrap-discovery", jobs.DiscoveryMaxObjects)
		if err != nil {
			h.logger.Error("failed to queue parameter discovery fallback", "err", err, "device_id", deviceID)
			return
		}
		h.logger.Info("parameter discovery falling back to other root", "device_id", deviceID,
			"tried_root", payload.Root, "fallback_root", payload.FallbackRoot, "command_key", fallback.CommandKey)
		if err := h.jobs.RecordDiscoveryFallback(ctx, job.ID, fallback.CommandKey); err != nil {
			h.logger.Error("record discovery fallback", "err", err)
		}
		return
	}

	list := body.GetParameterNamesResponse.ParameterList
	writableByName := make(map[string]bool, len(list))
	writableCount := 0
	for _, p := range list {
		writable := p.Writable == "1" || p.Writable == "true"
		writableByName[p.Name] = writable
		if writable {
			writableCount++
		}
	}
	if payload.Incremental {
		done, err := jobs.AdvanceDiscovery(&payload, writableByName)
		if err != nil {
			_ = h.jobs.MarkFailed(ctx, job.ID, "", err.Error())
			return
		}
		if !done {
			if err := h.jobs.RequeueDiscovery(ctx, job, payload); err != nil {
				h.logger.Error("persist discovery progress", "err", err)
			}
			return
		}
		writableByName = payload.Names
		writableCount = 0
		for _, writable := range writableByName {
			if writable {
				writableCount++
			}
		}
	}

	if err := h.params.SaveDiscovery(ctx, deviceID, parameters.DiscoverySnapshot{CommandKey: job.CommandKey, Protocol: "CWMP", Root: payload.Root, Names: writableByName, ExpectedFirmware: payload.Firmware}); err != nil {
		h.logger.Error("failed to save discovered parameter names", "err", err, "device_id", deviceID)
		_ = h.jobs.MarkFailed(ctx, job.ID, "", "failed to persist discovery snapshot")
		if errors.Is(err, parameters.ErrFirmwareChanged) {
			_ = h.jobs.QueueDiscoveryIfNeeded(ctx, deviceID)
		}
		return
	}

	root := devices.DataModelRootDevice2
	if payload.Root == rootIGD1 {
		root = devices.DataModelRootIGD1
	}
	if err := h.devices.UpdateDataModelRoot(ctx, deviceID, root); err != nil {
		h.logger.Error("failed to update data model root", "err", err, "device_id", deviceID)
	}

	if err := h.jobs.MarkSuccessWithDetail(ctx, job.ID, discoveryResultDetail{
		Root: payload.Root, ParameterCount: len(writableByName), WritableCount: writableCount,
	}); err != nil {
		h.logger.Error("failed to mark discovery job success", "err", err, "job_id", job.ID)
	}
	h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()

	if err := h.auditor.Record(ctx, "system", deviceID, "ParameterDiscoveryCompleted", map[string]any{
		"job_id": job.ID, "command_key": job.CommandKey, "root": payload.Root,
		"parameter_count": len(writableByName), "writable_count": writableCount,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("parameter discovery completed", "device_id", deviceID, "root", payload.Root,
		"parameter_count", len(writableByName), "writable_count", writableCount, "command_key", job.CommandKey)
}
