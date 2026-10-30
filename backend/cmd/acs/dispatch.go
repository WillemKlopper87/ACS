// Job -> RPC rendering and RPC response -> job completion (split out of
// main.go, audit P3.1).
package main

import (
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/diagnostics"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"acs/internal/tmf/telemetry"
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

// diagIPPingPollPaths is the poll read for a ping job queued before
// discovery-filtered plans existed: the state plus every result parameter
// TR-181/TR-098 both define for IPPing (same leaf names, different subtree
// root — adapters.DiagnosticsPrefix), so the parameter cache is populated
// the moment the diagnostic completes. New jobs carry their own PollPaths.
func diagIPPingPollPaths(prefix string) []string {
	return prefixed(prefix, append([]string{"DiagnosticsState"}, diagnostics.PingResultLeaves...))
}

// diagTraceroutePollPaths — see diagIPPingPollPaths. RouteHops.{i}.* is a
// dynamic-length table, so only RouteHopsNumberOfEntries is read back; an
// operator wanting per-hop detail follows up with an ordinary GET_PARAMETER.
func diagTraceroutePollPaths(prefix string) []string {
	return prefixed(prefix, append([]string{"DiagnosticsState"}, diagnostics.TracerouteResultLeaves...))
}

// diagTR143PollPaths is the legacy poll list (both directions' result
// leaves); new jobs poll only the leaves discovery found for their direction.
func diagTR143PollPaths(prefix string) []string {
	return prefixed(prefix, []string{"DiagnosticsState", "ROMTime", "BOMTime", "EOMTime",
		"TestBytesReceived", "TestBytesSent", "TotalBytesReceived", "TotalBytesSent",
		"TCPOpenRequestTime", "TCPOpenResponseTime"})
}

func prefixed(prefix string, leaves []string) []string {
	out := make([]string, len(leaves))
	for i, leaf := range leaves {
		out[i] = prefix + leaf
	}
	return out
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
		if payload.VerificationPending {
			return cwmp.RenderGetParameterValues(id, jobs.VerificationPaths(payload)), true
		}
		params := make([]cwmp.ParameterValueStruct, len(payload.Parameters))
		for i, p := range payload.Parameters {
			params[i] = cwmp.ParameterValueStruct{Name: p.Name, Value: p.Value, Type: p.Type}
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
		if !jobs.DiagnosticsTriggered(job) {
			// Trigger: TR-069 diagnostics aren't a dedicated RPC — the
			// ACS writes the diagnostic's input parameters and kicks it
			// off via an ordinary SetParameterValues with
			// DiagnosticsState=Requested (v3 §10.1). Once the CPE acks it
			// the job waits for a later session (internal/jobs/
			// diagnostics.go) and every later dispatch polls instead.
			return cwmp.RenderSetParameterValues(id, diagnosticTrigger(payload.Trigger, prefix,
				"Host", payload.Host,
				"NumberOfRepetitions", strconv.Itoa(payload.NumberOfRepetitions),
				"Timeout", strconv.Itoa(payload.Timeout),
				"DataBlockSize", strconv.Itoa(payload.DataBlockSize),
				"DSCP", strconv.Itoa(payload.DSCP),
			), job.CommandKey), true
		}
		// Poll: read DiagnosticsState (and the result parameters, so a
		// completed poll needs no further round-trip).
		return cwmp.RenderGetParameterValues(id, diagnosticPollPaths(payload.PollPaths, diagIPPingPollPaths(prefix))), true

	case jobs.TypeDiagnosticsTraceroute:
		var payload jobs.DiagnosticsTraceroutePayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		prefix := payload.Prefix
		if prefix == "" {
			prefix = defaultDiagPrefix(adapters.DiagnosticTraceroute)
		}
		if !jobs.DiagnosticsTriggered(job) {
			return cwmp.RenderSetParameterValues(id, diagnosticTrigger(payload.Trigger, prefix,
				"Host", payload.Host,
				"NumberOfTries", strconv.Itoa(payload.NumberOfTries),
				"Timeout", strconv.Itoa(payload.Timeout),
				"DataBlockSize", strconv.Itoa(payload.DataBlockSize),
				"DSCP", strconv.Itoa(payload.DSCP),
				"MaxHopCount", strconv.Itoa(payload.MaxHopCount),
			), job.CommandKey), true
		}
		return cwmp.RenderGetParameterValues(id, diagnosticPollPaths(payload.PollPaths, diagTraceroutePollPaths(prefix))), true

	case jobs.TypeDiagnosticsDownload, jobs.TypeDiagnosticsUpload:
		var payload struct {
			URL            string `json:"url"`
			TestFileLength uint64 `json:"test_file_length"`
			jobs.DiagnosticsPlan
		}
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		// jobs.DiagnosticsDownloadPayload/DiagnosticsUploadPayload share
		// these JSON fields, so the anonymous struct above decodes either
		// one without switching on job.Type first.
		prefix := diagnosticPayloadPrefix(job)
		if !jobs.DiagnosticsTriggered(job) {
			if job.Type == jobs.TypeDiagnosticsUpload {
				length := payload.TestFileLength
				if length == 0 {
					length = 1048576
				}
				return cwmp.RenderSetParameterValues(id, diagnosticTrigger(payload.Trigger, prefix,
					"UploadURL", payload.URL, "TestFileLength", strconv.FormatUint(length, 10)), job.CommandKey), true
			}
			return cwmp.RenderSetParameterValues(id, diagnosticTrigger(payload.Trigger, prefix, "DownloadURL", payload.URL), job.CommandKey), true
		}
		return cwmp.RenderGetParameterValues(id, diagnosticPollPaths(payload.PollPaths, diagTR143PollPaths(prefix))), true

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
		return cwmp.RenderGetParameterNames(id, payload.RequestPath(), payload.Incremental), true

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
	if job.DeviceID != deviceID || job.Status != jobs.StatusRPCSent {
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
		h.publishTMFFault(ctx, deviceID, job.ID, "CWMP", code, msg)
		return
	}

	switch job.Type {
	case jobs.TypeSetParameter:
		if body.SetParameterValuesResponse == nil {
			var payload jobs.SetParameterPayload
			if json.Unmarshal(job.Payload, &payload) == nil && payload.VerificationPending && body.GetParameterValuesResponse != nil {
				actual := map[string]string{}
				for _, v := range body.GetParameterValuesResponse.ParameterList {
					actual[v.Name] = v.Value
				}
				check := jobs.CompareReadback(payload, actual)
				success := len(check.Mismatched) == 0 && len(check.Missing) == 0 && len(check.Matched) > 0
				if err := h.params.RecordOperation(ctx, deviceID, job.CommandKey, "WRITE", success, jobs.VerificationPaths(payload), check, jobs.EvidenceFirmware(job)); err != nil {
					_ = h.jobs.MarkFailed(ctx, job.ID, "EVIDENCE_FAILED", "failed to persist write verification")
					return
				}
				h.cacheParameterValues(ctx, deviceID, body.GetParameterValuesResponse.ParameterList, parameters.SourceGetValues)
				if !success {
					_ = h.jobs.MarkFailed(ctx, job.ID, "READBACK_MISMATCH", "readback did not confirm every readable parameter")
					return
				}
				h.markJobSuccess(ctx, deviceID, job)
				return
			}
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		var verifyPayload jobs.SetParameterPayload
		if json.Unmarshal(job.Payload, &verifyPayload) == nil && verifyPayload.Verify && len(jobs.VerificationPaths(verifyPayload)) > 0 {
			if err := h.jobs.BeginReadback(ctx, job, verifyPayload); err != nil {
				_ = h.jobs.MarkFailed(ctx, job.ID, "READBACK_QUEUE_FAILED", "failed to queue verification")
			}
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
		var read jobs.GetParameterPayload
		actual := map[string]string{}
		for _, value := range body.GetParameterValuesResponse.ParameterList {
			actual[value.Name] = value.Value
		}
		if json.Unmarshal(job.Payload, &read) != nil || len(jobs.MissingReadPaths(read.Paths, actual)) > 0 {
			_ = h.jobs.MarkFailed(ctx, job.ID, "PARTIAL_READ", "device omitted requested readable parameters")
			return
		}
		h.markJobSuccess(ctx, deviceID, job)
		h.cacheParameterValues(ctx, deviceID, body.GetParameterValuesResponse.ParameterList, parameters.SourceGetValues)
		paths := []string{}
		for _, v := range body.GetParameterValuesResponse.ParameterList {
			paths = append(paths, v.Name)
		}
		if len(paths) > 0 {
			if err := h.params.RecordOperation(ctx, deviceID, job.CommandKey, "READ", true, paths, map[string]string{"protocol": "CWMP"}, jobs.EvidenceFirmware(job)); err != nil {
				h.logger.Error("failed to record read evidence", "err", err)
			}
		}

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

	case jobs.TypeDiagnosticsPing, jobs.TypeDiagnosticsTraceroute, jobs.TypeDiagnosticsDownload, jobs.TypeDiagnosticsUpload:
		h.completeDiagnostic(ctx, deviceID, job, body)

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
	if job.Type == jobs.TypeReboot || job.Type == jobs.TypeFactoryReset {
		// The CPE acknowledged the reboot/reset: it is expected to go silent
		// until it informs again, which is not an outage.
		if err := h.devices.MarkRebooting(ctx, deviceID); err != nil {
			h.logger.Error("failed to mark device rebooting", "err", err, "device_id", deviceID)
		}
	}
}

func (h *handler) markUnexpectedResponse(ctx context.Context, deviceID string, job *jobs.Job) {
	const msg = "response did not match the RPC that was sent"
	if err := h.jobs.MarkFailed(ctx, job.ID, "", msg); err != nil {
		h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
	}
	h.auditFailure(ctx, deviceID, job, "", msg)
}

// completeDiagnostic handles the CPE's answer to a diagnostic's trigger or
// poll (lifecycle in internal/jobs/diagnostics.go). An acknowledged trigger,
// or a poll that still reads a pending state, parks the job until the next
// Inform — never back to QUEUED within this session, where the CPE cannot
// have started the test yet.
func (h *handler) completeDiagnostic(ctx context.Context, deviceID string, job *jobs.Job, body cwmp.InboundBody) {
	if !jobs.DiagnosticsTriggered(job) {
		if body.SetParameterValuesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.awaitDiagnostic(ctx, deviceID, job, "triggered; awaiting 8 DIAGNOSTICS COMPLETE or the next session")
		return
	}
	if body.GetParameterValuesResponse == nil {
		h.markUnexpectedResponse(ctx, deviceID, job)
		return
	}
	list := body.GetParameterValuesResponse.ParameterList
	h.cacheParameterValues(ctx, deviceID, list, parameters.SourceGetValues)
	prefix := diagnosticPayloadPrefix(job)
	state := diagnosticsState(list, prefix)
	switch classifyDiagnosticsState(state) {
	case diagPending:
		h.awaitDiagnostic(ctx, deviceID, job, "DiagnosticsState still "+strconv.Quote(state))
	case diagComplete:
		if job.Type == jobs.TypeDiagnosticsDownload || job.Type == jobs.TypeDiagnosticsUpload {
			h.markTR143Success(ctx, deviceID, job, list, prefix, job.Type == jobs.TypeDiagnosticsUpload)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)
	default:
		// TR-181 error states, e.g. Error_CannotResolveHostName,
		// Error_Internal, Error_Other, Canceled.
		if err := h.jobs.MarkFailed(ctx, job.ID, "", state); err != nil {
			h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
		}
		h.auditFailure(ctx, deviceID, job, "", state)
	}
}

type diagStateClass int

const (
	diagPending diagStateClass = iota
	diagComplete
	diagFailed
)

// classifyDiagnosticsState maps a polled DiagnosticsState onto the job's
// next step. "None" counts as pending: a CPE may report it until the queued
// test actually starts, and the time deadline still ends a test the CPE
// really abandoned.
func classifyDiagnosticsState(state string) diagStateClass {
	switch state {
	case "", "Requested", "None":
		return diagPending
	case "Complete", "Completed":
		return diagComplete
	}
	return diagFailed
}

// awaitDiagnostic parks a diagnostic until the device's next Inform, unless
// its time deadline (or the max_attempts backstop) is exhausted, in which
// case it ends as TIMEOUT.
func (h *handler) awaitDiagnostic(ctx context.Context, deviceID string, job *jobs.Job, detail string) {
	if jobs.DiagnosticsExpired(job, time.Now()) || job.Attempts >= job.MaxAttempts {
		msg := "diagnostic did not complete by its deadline"
		if job.Attempts >= job.MaxAttempts {
			msg = "diagnostic did not complete within " + strconv.Itoa(job.MaxAttempts) + " attempts"
		}
		if err := h.jobs.MarkTimeout(ctx, job.ID, msg); err != nil {
			h.logger.Error("failed to mark job timeout", "err", err, "job_id", job.ID)
		}
		h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusTimeout).Inc()
		h.logger.Warn("diagnostic timed out", "job_id", job.ID, "command_key", job.CommandKey,
			"device_id", deviceID, "attempts", job.Attempts, "detail", msg)
		return
	}
	applied, err := h.jobs.MarkAwaitingDiagnostics(ctx, job, jobs.DiagnosticsWindow(job))
	if err != nil {
		h.logger.Error("failed to park diagnostic job", "err", err, "job_id", job.ID)
		return
	}
	if applied {
		h.logger.Info("diagnostic awaiting completion", "job_id", job.ID, "command_key", job.CommandKey,
			"device_id", deviceID, "attempts", job.Attempts, "detail", detail)
	}
}

// releaseAwaitingDiagnostics runs on every Inform: a new session is the
// earliest point a parked diagnostic may be polled, and releasing once per
// Inform keeps it to one poll per session.
func (h *handler) releaseAwaitingDiagnostics(ctx context.Context, deviceID string, diagnosticsComplete bool) {
	n, err := h.jobs.ReleaseAwaitingDiagnostics(ctx, deviceID)
	if err != nil {
		h.logger.Error("failed to release awaiting diagnostics", "err", err, "device_id", deviceID)
		return
	}
	if n > 0 {
		h.logger.Info("polling awaiting diagnostics in this session", "device_id", deviceID, "jobs", n,
			"diagnostics_complete_event", diagnosticsComplete)
	}
}

// diagnosticTrigger returns the trigger's SetParameterValues list: the plan
// resolved from discovery at job creation when there is one, otherwise (a
// job queued before plans existed) every standard input from pairs
// (leaf, value, ...), typed per TR-181, with DiagnosticsState last.
func diagnosticTrigger(planned []jobs.ParameterWrite, prefix string, pairs ...string) []cwmp.ParameterValueStruct {
	if len(planned) > 0 {
		params := make([]cwmp.ParameterValueStruct, len(planned))
		for i, p := range planned {
			params[i] = cwmp.ParameterValueStruct{Name: p.Name, Value: p.Value, Type: p.Type}
		}
		return params
	}
	params := make([]cwmp.ParameterValueStruct, 0, len(pairs)/2+1)
	for i := 0; i+1 < len(pairs); i += 2 {
		params = append(params, cwmp.ParameterValueStruct{Name: prefix + pairs[i], Value: pairs[i+1], Type: diagnostics.LeafType(pairs[i])})
	}
	return append(params, cwmp.ParameterValueStruct{Name: prefix + "DiagnosticsState", Value: "Requested", Type: diagnostics.LeafType("DiagnosticsState")})
}

// diagnosticPollPaths prefers the discovery-filtered poll list stored on the
// job over the full standard list.
func diagnosticPollPaths(planned, standard []string) []string {
	if len(planned) > 0 {
		return planned
	}
	return standard
}

// diagnosticPayloadPrefix resolves the diagnostic object a job targets.
func diagnosticPayloadPrefix(job *jobs.Job) string {
	var payload struct {
		Prefix string `json:"prefix"`
	}
	_ = json.Unmarshal(job.Payload, &payload)
	if payload.Prefix != "" {
		return payload.Prefix
	}
	switch job.Type {
	case jobs.TypeDiagnosticsTraceroute:
		return defaultDiagPrefix(adapters.DiagnosticTraceroute)
	case jobs.TypeDiagnosticsDownload:
		return "Device.IP.Diagnostics.DownloadDiagnostics."
	case jobs.TypeDiagnosticsUpload:
		return "Device.IP.Diagnostics.UploadDiagnostics."
	}
	return defaultDiagPrefix(adapters.DiagnosticPing)
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

// paramValue looks up one exact parameter name in a GetParameterValues
// response, or "" if absent — the CPE omits a result parameter it doesn't
// support rather than reporting it as empty, so absence is the normal case
// for TCPOpenRequestTime/TCPOpenResponseTime on many CPEs.
func paramValue(list []cwmp.ParameterValueStruct, name string) string {
	for _, p := range list {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// parseCWMPDateTime parses a TR-143 xsd:dateTime result parameter
// (ROMTime/BOMTime/EOMTime/TCPOpen*Time). CPEs report these in RFC3339,
// sometimes with fractional seconds; TR-069's own convention for "unknown"
// is the zero date 0001-01-01T00:00:00Z, which this treats as absent
// rather than a valid instant, since a duration computed against it would
// be meaningless.
func parseCWMPDateTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil && t.Year() > 1 {
			return t, true
		}
	}
	return time.Time{}, false
}

// tr143Result is the computed detail attached to a completed
// DIAGNOSTICS_DOWNLOAD/DIAGNOSTICS_UPLOAD job's result_detail (surfaced via
// GET /jobs/{command_key}), so a caller gets a usable throughput figure
// instead of having to re-derive it from raw ROM/BOM/EOM timestamps and
// byte counters themselves.
type tr143Result struct {
	Direction          string `json:"direction"`
	TestBytes          int64  `json:"test_bytes"`
	TestDurationMS     int64  `json:"test_duration_ms,omitempty"`
	ThroughputBps      int64  `json:"throughput_bps,omitempty"`
	TCPOpenTimeMS      int64  `json:"tcp_open_time_ms,omitempty"`
	ROMTime            string `json:"rom_time,omitempty"`
	BOMTime            string `json:"bom_time,omitempty"`
	EOMTime            string `json:"eom_time,omitempty"`
	TotalBytesSent     int64  `json:"total_bytes_sent,omitempty"`
	TotalBytesReceived int64  `json:"total_bytes_received,omitempty"`
}

// computeTR143Result turns a completed TR-143 poll's raw parameters into
// tr143Result. Test-duration/throughput are only populated when BOM and EOM
// both parsed and EOM is after BOM — a CPE that didn't populate them
// (common for those that only support the mandatory subset) still gets its
// raw byte counters reported, just without a derived rate.
func computeTR143Result(list []cwmp.ParameterValueStruct, prefix string, upload bool) tr143Result {
	result := tr143Result{Direction: map[bool]string{true: "upload", false: "download"}[upload]}
	testBytesName := prefix + "TestBytesReceived"
	if upload {
		testBytesName = prefix + "TestBytesSent"
	}
	result.TestBytes, _ = strconv.ParseInt(paramValue(list, testBytesName), 10, 64)
	result.TotalBytesSent, _ = strconv.ParseInt(paramValue(list, prefix+"TotalBytesSent"), 10, 64)
	result.TotalBytesReceived, _ = strconv.ParseInt(paramValue(list, prefix+"TotalBytesReceived"), 10, 64)

	result.ROMTime = paramValue(list, prefix+"ROMTime")
	result.BOMTime = paramValue(list, prefix+"BOMTime")
	result.EOMTime = paramValue(list, prefix+"EOMTime")
	bom, bomOK := parseCWMPDateTime(result.BOMTime)
	eom, eomOK := parseCWMPDateTime(result.EOMTime)
	if bomOK && eomOK && eom.After(bom) && result.TestBytes > 0 {
		duration := eom.Sub(bom)
		result.TestDurationMS = duration.Milliseconds()
		result.ThroughputBps = int64(float64(result.TestBytes*8) / duration.Seconds())
	}

	openReq, openReqOK := parseCWMPDateTime(paramValue(list, prefix+"TCPOpenRequestTime"))
	openResp, openRespOK := parseCWMPDateTime(paramValue(list, prefix+"TCPOpenResponseTime"))
	if openReqOK && openRespOK && openResp.After(openReq) {
		result.TCPOpenTimeMS = openResp.Sub(openReq).Milliseconds()
	}
	return result
}

// markTR143Success completes a DIAGNOSTICS_DOWNLOAD/DIAGNOSTICS_UPLOAD job
// with its computed throughput attached, mirroring how the AddObject case
// above attaches instance_number — the audit trail and job status both get
// the same detail a caller would otherwise have to recompute by hand.
func (h *handler) markTR143Success(ctx context.Context, deviceID string, job *jobs.Job, list []cwmp.ParameterValueStruct, prefix string, upload bool) {
	result := computeTR143Result(list, prefix, upload)
	if err := h.jobs.MarkSuccessWithDetail(ctx, job.ID, result); err != nil {
		h.logger.Error("failed to mark job success", "err", err, "job_id", job.ID)
	}
	h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()
	if err := h.auditor.Record(ctx, "system", deviceID, "JobSucceeded", map[string]any{
		"job_id": job.ID, "command_key": job.CommandKey, "type": job.Type,
		"test_bytes": result.TestBytes, "throughput_bps": result.ThroughputBps,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("job succeeded", "job_id", job.ID, "command_key", job.CommandKey, "type", job.Type,
		"device_id", deviceID, "throughput_bps", result.ThroughputBps)
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

func (h *handler) publishTMFFault(ctx context.Context, deviceID, jobID, protocol, code, message string) {
	if h.tmfEvents == nil {
		return
	}
	d, err := h.devices.Get(ctx, deviceID)
	if err != nil {
		h.logger.Warn("failed to load device for TMF fault", "error", err)
		return
	}
	account := ""
	if d.CustomerID != nil {
		account = *d.CustomerID
	}
	if err := telemetry.PublishFault(ctx, h.tmfEvents, telemetry.Fault{AccountID: account, DeviceID: deviceID, JobID: jobID, Protocol: protocol, Code: code, Message: message}); err != nil {
		h.logger.Warn("failed to publish TMF fault", "error", err, "device_id", deviceID)
	}
}
