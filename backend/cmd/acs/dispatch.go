// Job -> RPC rendering and RPC response -> job completion (split out of
// main.go, audit P3.1).
package main

import (
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"context"
	"encoding/json"
	"strconv"
	"time"
)

// defaultDiagPrefix resolves the backward-compatible prefix for a
// diagnostic job queued before jobs.DiagnosticsPingPayload/
// DiagnosticsTraceroutePayload carried their own Prefix field (empty
// Prefix means "assume what this codebase always assumed": TR-181,
// design doc v3 §3's "TR-181 first" convention). Every current caller
// (cmd/api's createDiagnosticsPing/createDiagnosticsTraceroute) resolves
// and stores a real Prefix at job-creation time via
// adapters.DiagnosticsPrefix and the target device's own discovered
// devices.DataModelRoot — this is only the fallback for a job that
// predates that field.
func defaultDiagPrefix(kind string) string {
	return adapters.DiagnosticsPrefix(devices.DataModelRootDevice2, kind)
}

// diagIPPingPollPaths is what a poll (attempts >= 2) reads back: the
// state plus every result parameter TR-181/TR-098 both define for IPPing
// (same leaf names, different subtree root — adapters.DiagnosticsPrefix),
// so the parameter cache ends up populated the moment the diagnostic
// completes rather than needing a second round-trip.
func diagIPPingPollPaths(prefix string) []string {
	return []string{
		prefix + "DiagnosticsState",
		prefix + "SuccessCount",
		prefix + "FailureCount",
		prefix + "AverageResponseTime",
		prefix + "MinimumResponseTime",
		prefix + "MaximumResponseTime",
	}
}

// diagTraceroutePollPaths — see diagIPPingPollPaths. RouteHops.{i}.* is a
// dynamic-length table TR-069 itself defines with no fixed path list to
// poll, so only RouteHopsNumberOfEntries is read back here — an operator
// wanting per-hop detail can follow up with an ordinary GET_PARAMETER
// using that count, the same way this ACS already leaves WiFi
// associated-device detail to GET_PARAMETER rather than a dedicated
// parser.
func diagTraceroutePollPaths(prefix string) []string {
	return []string{
		prefix + "DiagnosticsState",
		prefix + "ResponseTime",
		prefix + "RouteHopsNumberOfEntries",
	}
}

// renderJobRequest turns a leased job into the CWMP RPC request bytes to
// send the CPE.
func (h *handler) renderJobRequest(job *jobs.Job) (body []byte, ok bool) {
	id := cwmp.NewID()
	switch job.Type {
	case jobs.TypeSetParameter:
		var payload jobs.SetParameterPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		params := make([]cwmp.ParameterValueStruct, len(payload.Parameters))
		for i, p := range payload.Parameters {
			params[i] = cwmp.ParameterValueStruct{Name: p.Name, Value: p.Value}
		}
		return cwmp.RenderSetParameterValues(id, params, job.CommandKey), true

	case jobs.TypeGetParameter:
		var payload jobs.GetParameterPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderGetParameterValues(id, payload.Paths), true

	case jobs.TypeFirmwareDownload:
		var payload jobs.FirmwareDownloadPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderDownload(id, job.CommandKey, payload.FileType, payload.URL,
			payload.Username, payload.Password, payload.FileSize, payload.TargetFilename, payload.DelaySeconds), true

	case jobs.TypeDiagnosticsPing:
		var payload jobs.DiagnosticsPingPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		prefix := payload.Prefix
		if prefix == "" {
			prefix = defaultDiagPrefix(adapters.DiagnosticPing)
		}
		if job.Attempts <= 1 {
			// Trigger: TR-069 diagnostics aren't a dedicated RPC — the
			// ACS writes the diagnostic's input parameters and kicks it
			// off via an ordinary SetParameterValues with
			// DiagnosticsState=Requested (v3 §10.1). Later attempts poll
			// instead (see below); attempts counts from Lease, so 1 means
			// this is the very first dispatch.
			params := []cwmp.ParameterValueStruct{
				{Name: prefix + "Host", Value: payload.Host},
				{Name: prefix + "NumberOfRepetitions", Value: strconv.Itoa(payload.NumberOfRepetitions)},
				{Name: prefix + "Timeout", Value: strconv.Itoa(payload.Timeout)},
				{Name: prefix + "DataBlockSize", Value: strconv.Itoa(payload.DataBlockSize)},
				{Name: prefix + "DSCP", Value: strconv.Itoa(payload.DSCP)},
				{Name: prefix + "DiagnosticsState", Value: "Requested"},
			}
			return cwmp.RenderSetParameterValues(id, params, job.CommandKey), true
		}
		// Poll: read DiagnosticsState (and the result parameters, so a
		// completed poll needs no further round-trip) until it leaves
		// "Requested".
		return cwmp.RenderGetParameterValues(id, diagIPPingPollPaths(prefix)), true

	case jobs.TypeDiagnosticsTraceroute:
		var payload jobs.DiagnosticsTraceroutePayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		prefix := payload.Prefix
		if prefix == "" {
			prefix = defaultDiagPrefix(adapters.DiagnosticTraceroute)
		}
		if job.Attempts <= 1 {
			params := []cwmp.ParameterValueStruct{
				{Name: prefix + "Host", Value: payload.Host},
				{Name: prefix + "NumberOfTries", Value: strconv.Itoa(payload.NumberOfTries)},
				{Name: prefix + "Timeout", Value: strconv.Itoa(payload.Timeout)},
				{Name: prefix + "DataBlockSize", Value: strconv.Itoa(payload.DataBlockSize)},
				{Name: prefix + "DSCP", Value: strconv.Itoa(payload.DSCP)},
				{Name: prefix + "MaxHopCount", Value: strconv.Itoa(payload.MaxHopCount)},
				{Name: prefix + "DiagnosticsState", Value: "Requested"},
			}
			return cwmp.RenderSetParameterValues(id, params, job.CommandKey), true
		}
		return cwmp.RenderGetParameterValues(id, diagTraceroutePollPaths(prefix)), true

	case jobs.TypeAddObject:
		var payload jobs.AddObjectPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderAddObject(id, payload.ObjectPath, job.CommandKey), true

	case jobs.TypeDeleteObject:
		var payload jobs.DeleteObjectPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderDeleteObject(id, payload.ObjectPath, job.CommandKey), true

	case jobs.TypeReboot:
		return cwmp.RenderReboot(id, job.CommandKey), true

	case jobs.TypeFactoryReset:
		return cwmp.RenderFactoryReset(id), true

	case jobs.TypeScheduleInform:
		var payload jobs.ScheduleInformPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderScheduleInform(id, job.CommandKey, payload.DelaySeconds), true

	case jobs.TypeSetParameterAttributes:
		var payload jobs.SetParameterAttributesPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		attrs := make([]cwmp.AttributeWrite, len(payload.Attributes))
		for i, a := range payload.Attributes {
			attrs[i] = cwmp.AttributeWrite{Name: a.Name, Notification: a.Notification}
		}
		return cwmp.RenderSetParameterAttributes(id, attrs), true

	case jobs.TypeGetParameterAttributes:
		var payload jobs.GetParameterAttributesPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderGetParameterAttributes(id, payload.Paths), true

	case jobs.TypeUpload:
		var payload jobs.UploadPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderUpload(id, job.CommandKey, payload.FileType, payload.URL, payload.Username, payload.Password, payload.DelaySeconds), true

	case jobs.TypeParameterDiscovery:
		var payload jobs.ParameterDiscoveryPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderGetParameterNames(id, payload.Root, false), true

	default:
		return nil, false
	}
}

// completeJob records the CPE's response (or fault) against whichever job
// was in flight, updates the parameter cache when applicable, and — on a
// successful SET_PARAMETER — queues a GET_PARAMETER confirmation job so
// the cache ends up reflecting what the CPE actually reports rather than
// what the ACS assumed was applied (the same "don't trust the ack, verify
// afterwards" pattern v3 §9.3 uses for TransferComplete -> SoftwareVersion,
// applied here to ordinary parameter writes).
func (h *handler) completeJob(ctx context.Context, deviceID, jobID string, body cwmp.InboundBody) {
	job, err := h.jobs.ByID(ctx, jobID)
	if err != nil {
		h.logger.Error("failed to load in-flight job", "err", err, "job_id", jobID)
		return
	}

	// PARAMETER_DISCOVERY handles its own fault path (a fault or an empty
	// name list both mean "wrong root, try the other one" — see
	// completeParameterDiscovery) instead of the generic fault-means-FAILED
	// handling below, so it's special-cased ahead of that check.
	if job.Type == jobs.TypeParameterDiscovery {
		h.completeParameterDiscovery(ctx, deviceID, job, body)
		return
	}

	if body.Fault != nil {
		code, msg := body.Fault.CWMPCode(), body.Fault.CWMPMessage()
		if err := h.jobs.MarkFailed(ctx, job.ID, code, msg); err != nil {
			h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
		}
		h.auditFailure(ctx, deviceID, job, code, msg)
		return
	}

	switch job.Type {
	case jobs.TypeSetParameter:
		if body.SetParameterValuesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

		var payload jobs.SetParameterPayload
		if err := json.Unmarshal(job.Payload, &payload); err == nil && len(payload.Parameters) > 0 {
			values := make(map[string]parameters.CachedValue, len(payload.Parameters))
			paths := make([]string, len(payload.Parameters))
			for i, p := range payload.Parameters {
				values[p.Name] = parameters.CachedValue{Value: p.Value, Type: p.Type, UpdatedAt: time.Now().UTC(), Source: parameters.SourceSetValues}
				paths[i] = p.Name
			}
			if err := h.params.Upsert(ctx, deviceID, values); err != nil {
				h.logger.Error("failed to upsert parameter cache", "err", err, "device_id", deviceID)
			}

			confirm, err := h.jobs.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, "system:confirm")
			if err != nil {
				h.logger.Error("failed to queue confirmation read", "err", err, "device_id", deviceID)
			} else {
				h.metrics.JobsCreatedTotal.WithLabelValues(jobs.TypeGetParameter).Inc()
				h.logger.Info("queued confirmation read", "job_id", confirm.ID, "command_key", confirm.CommandKey, "device_id", deviceID)
			}
		}

	case jobs.TypeGetParameter:
		if body.GetParameterValuesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)
		h.cacheParameterValues(ctx, deviceID, body.GetParameterValuesResponse.ParameterList, parameters.SourceGetValues)

	case jobs.TypeFirmwareDownload:
		if body.DownloadResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		// Not SUCCESS: DownloadResponse only means the CPE accepted the
		// request (v3 §9.2/§19.7) — the real outcome arrives later as
		// TransferComplete, handled by handleTransferComplete above,
		// correlated by CommandKey rather than this session.
		if err := h.jobs.MarkAwaitingTransferComplete(ctx, job.ID); err != nil {
			h.logger.Error("failed to mark job awaiting transfer complete", "err", err, "job_id", job.ID)
		}
		h.logger.Info("firmware download accepted, awaiting transfer complete",
			"job_id", job.ID, "command_key", job.CommandKey, "device_id", deviceID, "status", body.DownloadResponse.Status)

	case jobs.TypeUpload:
		if body.UploadResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		// Same "accepted now, TransferComplete confirms later" shape as
		// FIRMWARE_DOWNLOAD — the actual file arrives at cmd/api's
		// upload-receive endpoint independently of this session.
		if err := h.jobs.MarkAwaitingTransferComplete(ctx, job.ID); err != nil {
			h.logger.Error("failed to mark job awaiting transfer complete", "err", err, "job_id", job.ID)
		}
		h.logger.Info("upload accepted, awaiting transfer complete",
			"job_id", job.ID, "command_key", job.CommandKey, "device_id", deviceID, "status", body.UploadResponse.Status)

	case jobs.TypeDiagnosticsPing:
		if job.Attempts <= 1 {
			// The trigger (SetParameterValues) was acked — the CPE has
			// accepted the request, not run it yet. Requeue so the next
			// dispatch on this device polls instead of re-triggering.
			if body.SetParameterValuesResponse == nil {
				h.markUnexpectedResponse(ctx, deviceID, job)
				return
			}
			h.requeueDiagnostic(ctx, deviceID, job, "triggered, awaiting result")
			return
		}

		// A poll (GetParameterValues) came back.
		if body.GetParameterValuesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		list := body.GetParameterValuesResponse.ParameterList
		h.cacheParameterValues(ctx, deviceID, list, parameters.SourceGetValues)

		var pingPayload jobs.DiagnosticsPingPayload
		if err := json.Unmarshal(job.Payload, &pingPayload); err != nil {
			h.logger.Error("failed to unmarshal diagnostics ping payload; assuming TR-181", "err", err, "job_id", job.ID)
		}
		pingPrefix := pingPayload.Prefix
		if pingPrefix == "" {
			pingPrefix = defaultDiagPrefix(adapters.DiagnosticPing)
		}

		switch state := diagnosticsState(list, pingPrefix); state {
		case "Requested", "":
			// Still running (or the CPE hasn't populated the state param
			// yet) — poll again.
			h.requeueDiagnostic(ctx, deviceID, job, "still running")
		case "Complete":
			h.markJobSuccess(ctx, deviceID, job)
		default:
			// TR-181 error states, e.g. Error_CannotResolveHostName,
			// Error_Internal, Error_Other.
			if err := h.jobs.MarkFailed(ctx, job.ID, "", state); err != nil {
				h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
			}
			h.auditFailure(ctx, deviceID, job, "", state)
		}

	case jobs.TypeDiagnosticsTraceroute:
		if job.Attempts <= 1 {
			if body.SetParameterValuesResponse == nil {
				h.markUnexpectedResponse(ctx, deviceID, job)
				return
			}
			h.requeueDiagnostic(ctx, deviceID, job, "triggered, awaiting result")
			return
		}

		if body.GetParameterValuesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		list := body.GetParameterValuesResponse.ParameterList
		h.cacheParameterValues(ctx, deviceID, list, parameters.SourceGetValues)

		var tracePayload jobs.DiagnosticsTraceroutePayload
		if err := json.Unmarshal(job.Payload, &tracePayload); err != nil {
			h.logger.Error("failed to unmarshal diagnostics traceroute payload; assuming TR-181", "err", err, "job_id", job.ID)
		}
		tracePrefix := tracePayload.Prefix
		if tracePrefix == "" {
			tracePrefix = defaultDiagPrefix(adapters.DiagnosticTraceroute)
		}

		switch state := diagnosticsState(list, tracePrefix); state {
		case "Requested", "":
			h.requeueDiagnostic(ctx, deviceID, job, "still running")
		case "Complete":
			h.markJobSuccess(ctx, deviceID, job)
		default:
			if err := h.jobs.MarkFailed(ctx, job.ID, "", state); err != nil {
				h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
			}
			h.auditFailure(ctx, deviceID, job, "", state)
		}

	case jobs.TypeAddObject:
		if body.AddObjectResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		if err := h.jobs.MarkSuccessWithDetail(ctx, job.ID, map[string]any{"instance_number": body.AddObjectResponse.InstanceNumber}); err != nil {
			h.logger.Error("failed to mark job success", "err", err, "job_id", job.ID)
		}
		h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()
		if err := h.auditor.Record(ctx, "system", deviceID, "JobSucceeded", map[string]any{
			"job_id": job.ID, "command_key": job.CommandKey, "type": job.Type, "instance_number": body.AddObjectResponse.InstanceNumber,
		}); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
		h.logger.Info("job succeeded", "job_id", job.ID, "command_key", job.CommandKey, "type", job.Type, "device_id", deviceID, "instance_number", body.AddObjectResponse.InstanceNumber)

	case jobs.TypeDeleteObject:
		if body.DeleteObjectResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeReboot:
		if body.RebootResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		// The CPE accepted the reboot request — the real confirmation is
		// the fresh Inform that follows once it comes back up (event code
		// "M Reboot"), the same "accepted now, confirmed later" shape
		// Download/TransferComplete already established. There's no
		// further RPC to correlate that Inform against; the job is done
		// once the CPE has acknowledged it will reboot.
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeFactoryReset:
		if body.FactoryResetResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeScheduleInform:
		if body.ScheduleInformResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeSetParameterAttributes:
		if body.SetParameterAttributesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeGetParameterAttributes:
		if body.GetParameterAttributesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		detail := make(map[string]any, len(body.GetParameterAttributesResponse.ParameterList))
		for _, a := range body.GetParameterAttributesResponse.ParameterList {
			detail[a.Name] = map[string]any{"notification": a.Notification, "access_list": a.AccessList}
		}
		if err := h.jobs.MarkSuccessWithDetail(ctx, job.ID, detail); err != nil {
			h.logger.Error("failed to mark job success", "err", err, "job_id", job.ID)
		}
		h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()

	default:
		h.logger.Error("completed job has unknown type", "job_id", job.ID, "type", job.Type)
	}
}

func (h *handler) markJobSuccess(ctx context.Context, deviceID string, job *jobs.Job) {
	if err := h.jobs.MarkSuccess(ctx, job.ID); err != nil {
		h.logger.Error("failed to mark job success", "err", err, "job_id", job.ID)
	}
	h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()
	if err := h.auditor.Record(ctx, "system", deviceID, "JobSucceeded", map[string]any{
		"job_id": job.ID, "command_key": job.CommandKey, "type": job.Type,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("job succeeded", "job_id", job.ID, "command_key", job.CommandKey, "type", job.Type, "device_id", deviceID)
}

func (h *handler) markUnexpectedResponse(ctx context.Context, deviceID string, job *jobs.Job) {
	const msg = "response did not match the RPC that was sent"
	if err := h.jobs.MarkFailed(ctx, job.ID, "", msg); err != nil {
		h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
	}
	h.auditFailure(ctx, deviceID, job, "", msg)
}

// requeueDiagnostic cycles a DIAGNOSTICS_PING job back to QUEUED for
// another poll, unless it has exhausted max_attempts — without a cap, a
// device whose DiagnosticsState never leaves "Requested" would poll
// forever (build plan §4 Phase 5).
func (h *handler) requeueDiagnostic(ctx context.Context, deviceID string, job *jobs.Job, detail string) {
	if job.Attempts >= job.MaxAttempts {
		msg := "diagnostic did not complete within " + strconv.Itoa(job.MaxAttempts) + " attempts"
		if err := h.jobs.MarkTimeout(ctx, job.ID, msg); err != nil {
			h.logger.Error("failed to mark job timeout", "err", err, "job_id", job.ID)
		}
		h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusTimeout).Inc()
		h.logger.Warn("diagnostic exhausted max attempts", "job_id", job.ID, "command_key", job.CommandKey,
			"device_id", deviceID, "attempts", job.Attempts, "max_attempts", job.MaxAttempts)
		return
	}
	if err := h.jobs.Requeue(ctx, job.ID); err != nil {
		h.logger.Error("failed to requeue diagnostic job", "err", err, "job_id", job.ID)
		return
	}
	h.logger.Info("diagnostic requeued for poll", "job_id", job.ID, "command_key", job.CommandKey,
		"device_id", deviceID, "attempts", job.Attempts, "detail", detail)
}

// diagnosticsState reads a diagnostic's DiagnosticsState parameter out of
// a GetParameterValues response (prefix selects IPPing vs TraceRoute's
// subtree), or "" if the CPE didn't include it.
func diagnosticsState(list []cwmp.ParameterValueStruct, prefix string) string {
	for _, p := range list {
		if p.Name == prefix+"DiagnosticsState" {
			return p.Value
		}
	}
	return ""
}

func (h *handler) auditFailure(ctx context.Context, deviceID string, job *jobs.Job, code, msg string) {
	h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusFailed).Inc()
	if err := h.auditor.Record(ctx, "system", deviceID, "JobFailed", map[string]any{
		"job_id": job.ID, "command_key": job.CommandKey, "type": job.Type,
		"fault_code": code, "fault_string": msg,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Warn("job failed", "job_id", job.ID, "command_key", job.CommandKey, "type", job.Type,
		"device_id", deviceID, "fault_code", code, "fault_string", msg)
}
