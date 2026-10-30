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
	"acs/internal/devices/adapters"
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
	if active, err := h.jobs.ActiveDiscovery(ctx, device.ID); err != nil {
		h.logger.Error("check active parameter discovery", "err", err, "device_id", device.ID)
		return
	} else if active != nil {
		h.logger.Info("parameter discovery already active; not queuing another", "device_id", device.ID, "command_key", active.CommandKey)
		return
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
	Root           string   `json:"root"`
	ParameterCount int      `json:"parameter_count"`
	WritableCount  int      `json:"writable_count"`
	Skipped        []string `json:"skipped,omitempty"`
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

	// A fault on one object deep in an incremental walk says that subtree is
	// unservable on this firmware, not that the root is wrong: skip it and
	// keep walking. Root-level faults still take the fallback path below.
	if payload.Incremental && payload.Cursor != "" && body.Fault != nil {
		h.skipDiscoveryObject(ctx, deviceID, job, payload, "fault "+body.Fault.CWMPCode()+": "+body.Fault.CWMPMessage())
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
	for _, p := range list {
		writableByName[p.Name] = p.Writable == "1" || p.Writable == "true"
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
	}

	h.finishParameterDiscovery(ctx, deviceID, job, payload, writableByName)
}

// finishParameterDiscovery persists a completed walk: the snapshot, the
// confirmed data-model root, the job result (including any objects skipped
// because the CPE could not serve them) and the audit record.
func (h *handler) finishParameterDiscovery(ctx context.Context, deviceID string, job *jobs.Job, payload jobs.ParameterDiscoveryPayload, writableByName map[string]bool) {
	writableCount := 0
	for _, writable := range writableByName {
		if writable {
			writableCount++
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
		Root: payload.Root, ParameterCount: len(writableByName), WritableCount: writableCount, Skipped: payload.Skipped,
	}); err != nil {
		h.logger.Error("failed to mark discovery job success", "err", err, "job_id", job.ID)
	}
	h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()

	if err := h.auditor.Record(ctx, "system", deviceID, "ParameterDiscoveryCompleted", map[string]any{
		"job_id": job.ID, "command_key": job.CommandKey, "root": payload.Root,
		"parameter_count": len(writableByName), "writable_count": writableCount, "skipped": payload.Skipped,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("parameter discovery completed", "device_id", deviceID, "root", payload.Root,
		"parameter_count", len(writableByName), "writable_count", writableCount, "skipped", len(payload.Skipped), "command_key", job.CommandKey)

	// Read the non-secret Wi-Fi values (SSID names, guest flags, AccessPoint
	// references, radio bands) once the tree is known, so the resolved Wi-Fi
	// network view is complete before an operator or BSS order needs it.
	if payload.Root == rootDevice2 {
		if paths := adapters.WiFiNetworkReadPaths(writableByName); len(paths) > 0 {
			if _, err := h.jobs.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, "system:wifi-state"); err != nil {
				h.logger.Error("failed to queue Wi-Fi state read", "err", err, "device_id", deviceID)
			}
		}
	}
}

// skipDiscoveryObject defers (first failure) or abandons (second failure)
// the object the walk is on (see jobs.SkipDiscoveryCursor) and either
// requeues the walk at the next object or, when nothing is left, completes
// discovery with what was found.
func (h *handler) skipDiscoveryObject(ctx context.Context, deviceID string, job *jobs.Job, payload jobs.ParameterDiscoveryPayload, reason string) {
	object := payload.Cursor
	done, skipped, err := jobs.SkipDiscoveryCursor(&payload)
	if err != nil {
		_ = h.jobs.MarkFailed(ctx, job.ID, "", err.Error())
		h.logger.Warn("parameter discovery abandoned", "device_id", deviceID, "command_key", job.CommandKey, "err", err)
		return
	}
	if skipped {
		h.logger.Warn("parameter discovery skipped an object the CPE could not serve",
			"device_id", deviceID, "object", object, "reason", reason, "attempt", 2, "command_key", job.CommandKey)
	} else {
		h.logger.Info("parameter discovery deferred an object for one retry",
			"device_id", deviceID, "object", object, "reason", reason, "command_key", job.CommandKey)
	}
	if done {
		h.finishParameterDiscovery(ctx, deviceID, job, payload, payload.Names)
		return
	}
	if err := h.jobs.RequeueDiscovery(ctx, job, payload); err != nil {
		h.logger.Error("persist discovery progress", "err", err, "job_id", job.ID)
	}
}

// recoverAbandonedDiscovery runs on every Inform. A CPE has one session at a
// time, so a new Inform means any GetParameterNames still RPC_SENT from an
// earlier session was never answered: the CPE dropped the session on that
// object. Skip it now rather than letting the 15-minute lease expire and the
// reaper resend the same object, which would stall discovery indefinitely.
func (h *handler) recoverAbandonedDiscovery(ctx context.Context, deviceID string) {
	inFlight, err := h.jobs.InFlightDiscoveries(ctx, deviceID)
	if err != nil {
		h.logger.Error("load in-flight discovery", "err", err, "device_id", deviceID)
		return
	}
	for _, job := range inFlight {
		var payload jobs.ParameterDiscoveryPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil || !payload.Incremental || payload.Cursor == "" {
			continue // root-level or legacy full-tree walks keep the lease/reaper path
		}
		h.skipDiscoveryObject(ctx, deviceID, job, payload, "CPE ended the session without responding")
	}
}
