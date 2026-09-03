// CWMP session handling: the HTTP entry point, Inform, the per-request
// dispatch loop, TransferComplete, and session close (split out of
// main.go, audit P3.1).
package main

import (
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"context"
	"io"
	"net"
	"net/http"
	"time"
)

// remoteIP strips the port from r.RemoteAddr so a client is rate-limited
// per address, not per ephemeral source port.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// handleCWMP implements the Phase 2 session shape:
//
//	Inform            -> upsert device, open session, InformResponse
//	anything else     -> complete any in-flight job's response, then
//	                     dispatch the next queued job or close the session
func (h *handler) handleCWMP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "CWMP requires POST", http.StatusMethodNotAllowed)
		return
	}
	h.onboardingListener.observe(r)

	// Coarse per-IP limit, ahead of auth and body parsing entirely — the
	// cheapest possible rejection for an unauthenticated flood (build
	// plan §7.1/§7c). A misbehaving *authenticated* device is caught
	// below, per-device, after we know who it is.
	if !h.ipLimiter.Allow(remoteIP(r)) {
		h.metrics.RateLimitRejectedTotal.Inc()
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	if r.URL.Path != "/cwmp" {
		h.logger.Debug("CWMP POST on non-standard path (device ACS URL differs from /cwmp)", "path", r.URL.Path, "remote", r.RemoteAddr)
	}

	// A presented client cert has already been chain-verified by the TLS
	// handshake itself (server.TLSConfig's ClientCAs, set only when
	// ACS_MTLS_CA_CERT is configured) — mTLS is the *preferred* method
	// per v3 §11.2, so it supersedes Digest for this request rather than
	// requiring both.
	authMode := devices.AuthModeNone
	mtlsAuthenticated := r.TLS != nil && len(r.TLS.PeerCertificates) > 0
	if mtlsAuthenticated {
		authMode = devices.AuthModeMTLS
	} else if h.auth.Enabled() {
		if ok, stale := h.auth.Verify(r); !ok {
			h.logger.Warn("authentication failed or missing", "remote", r.RemoteAddr)
			// Drain the unauthenticated request body before writing the
			// 401 so the keep-alive connection survives: an Inform can
			// exceed the amount net/http is willing to auto-discard, and
			// a closed connection here makes some CPEs treat the whole
			// session as failed instead of retrying with credentials.
			_, _ = io.Copy(io.Discard, r.Body)
			if stale {
				h.auth.ChallengeStale(w) // right credentials, expired nonce — retry, don't fail
			} else {
				h.auth.Challenge(w)
			}
			return
		}
		authMode = devices.AuthModeDigest
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.Warn("failed to read request body (possibly oversized)", "err", err, "remote", r.RemoteAddr)
		http.Error(w, "body too large or unreadable", http.StatusBadRequest)
		return
	}

	env, err := cwmp.ParseEnvelope(raw)
	if err != nil {
		h.logger.Error("malformed SOAP/XML", "err", err, "remote", r.RemoteAddr, "body", string(raw))
		http.Error(w, "malformed XML", http.StatusBadRequest)
		return
	}

	// Per-device limit, keyed on whatever identifies the device at this
	// point in the exchange: the natural key on Inform (before a session
	// cookie exists), the session cookie for every request after that —
	// which is 1:1 with a device for that session's lifetime, so it's
	// deviceID in effect without a DB lookup on every request just to
	// rate-limit. Generous burst (see defaultDeviceRateLimitBurst) so a
	// legitimate diagnostics poll loop (Phase 5) — several requests in
	// quick succession within one session — isn't mistaken for abuse.
	deviceKey := remoteIP(r)
	if env.Body.Inform != nil {
		deviceKey = env.Body.Inform.DeviceId.NaturalKey()
	} else if cookie, err := r.Cookie("acs_session"); err == nil {
		deviceKey = cookie.Value
	}
	if !h.deviceLimiter.Allow(deviceKey) {
		h.metrics.RateLimitRejectedTotal.Inc()
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	ctx := r.Context()

	// Responses to CPE-initiated RPCs must echo the request's cwmp:ID
	// (TR-069 §3.4.1.1) and speak the same CWMP namespace version the CPE
	// used — strict CPE stacks abort the session on either mismatch.
	respID := env.Header.ID
	if respID == "" {
		respID = cwmp.NewID()
	}
	ns := cwmp.DetectCWMPNamespace(raw)

	if env.Body.Inform != nil {
		h.handleInform(ctx, w, r, env.Body.Inform, authMode, respID, ns)
		return
	}

	// TransferComplete is checked ahead of the normal session-dispatch
	// path because it doesn't correlate to session.CurrentJobID at all —
	// the CPE may not send it until a session well after the one that
	// dispatched Download (fetch, flash, and reboot can take minutes),
	// so the only thing tying it back to a job is the CommandKey inside
	// the message itself (build plan §4 Phase 4).
	if env.Body.TransferComplete != nil {
		h.handleTransferComplete(ctx, w, env.Body.TransferComplete, respID, ns)
		return
	}

	h.dispatch(ctx, w, r, env.Body)
}

func (h *handler) handleInform(ctx context.Context, w http.ResponseWriter, r *http.Request, inform *cwmp.Inform, authMode, respID, ns string) {
	h.metrics.InformsTotal.Inc()
	events := inform.EventCodes()

	device, err := h.devices.UpsertFromInform(ctx, inform.DeviceId, events)
	if err != nil {
		h.logger.Error("failed to upsert device", "err", err, "oui_serial", inform.DeviceId.NaturalKey())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.devices.UpdateAuthMode(ctx, device.ID, authMode); err != nil {
		h.logger.Error("failed to record device auth mode", "err", err, "device_id", device.ID)
	}

	if len(inform.ParameterList) > 0 {
		h.cacheParameterValues(ctx, device.ID, inform.ParameterList, parameters.SourceInform)

		if url := extractConnectionRequestURL(inform.ParameterList); url != "" {
			if err := h.devices.UpdateConnectionRequestURL(ctx, device.ID, url); err != nil {
				h.logger.Error("failed to update connection request url", "err", err, "device_id", device.ID)
			}
		}

		if addr, natDetected, ok := extractSTUNStatus(inform.ParameterList); ok {
			if err := h.devices.UpdateSTUNStatus(ctx, device.ID, addr, natDetected); err != nil {
				h.logger.Error("failed to update stun status", "err", err, "device_id", device.ID)
			}
		}
	}

	h.correlateValueChangeEvents(ctx, device, inform.Event, inform.ParameterList)
	h.enforcePolicies(ctx, device, inform.ParameterList)

	if inform.HasEventCode("0") {
		h.applyAutoProvisioningTemplates(ctx, device)
		h.autoDiscoverParameters(ctx, device)
	}

	if inform.HasEventCode("6") {
		if err := h.devices.MarkInformedAfterConnectionRequest(ctx, device.ID); err != nil {
			h.logger.Error("failed to mark informed-after-connection-request", "err", err, "device_id", device.ID)
		}
	}

	session, err := h.sessions.Open(ctx, device.ID, events)
	if err != nil {
		h.logger.Error("failed to open session", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.metrics.SessionsOpenedTotal.Inc()

	if err := h.auditor.Record(ctx, "system", device.ID, "Inform", map[string]any{
		"event_codes": events,
		"session_id":  session.ID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	h.logger.Info("Inform received",
		"device_id", device.ID,
		"oui_serial", device.OUISerial,
		"manufacturer", device.Manufacturer,
		"events", events,
		"session_id", session.ID,
	)
	h.onboardingListener.onboarded(device.ID, r.RemoteAddr)

	http.SetCookie(w, &http.Cookie{
		Name:     "acs_session",
		Value:    session.ID,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		HttpOnly: true,
	})
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cwmp.RenderInformResponseNS(respID, ns))
}

// dispatch handles every non-Inform POST on an open session: if a job's
// RPC was already in flight, complete it with this response/fault; then
// try to lease the next queued job for the device and send its RPC, or
// close the session if there's no more work (design doc v3 §5.4).
func (h *handler) dispatch(ctx context.Context, w http.ResponseWriter, r *http.Request, body cwmp.InboundBody) {
	cookie, err := r.Cookie("acs_session")
	if err != nil {
		h.logger.Warn("request outside a known session (missing session cookie); nothing to do", "remote", r.RemoteAddr)
		h.respondEmpty(w)
		return
	}

	session, err := h.sessions.Get(ctx, cookie.Value)
	if err != nil {
		h.logger.Warn("request for unknown session", "session_id", cookie.Value, "remote", r.RemoteAddr)
		h.respondEmpty(w)
		return
	}
	if session.IsClosed() {
		h.logger.Warn("request for already-closed session", "session_id", cookie.Value, "remote", r.RemoteAddr)
		h.respondEmpty(w)
		return
	}

	if session.CurrentJobID != nil {
		h.completeJob(ctx, session.DeviceID, *session.CurrentJobID, body)
		if err := h.sessions.SetCurrentJob(ctx, session.ID, ""); err != nil {
			h.logger.Error("failed to clear in-flight job", "err", err, "session_id", session.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	job, err := h.jobs.Lease(ctx, session.DeviceID)
	if err != nil {
		h.logger.Error("failed to lease next job", "err", err, "device_id", session.DeviceID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if job == nil {
		h.closeSession(ctx, w, session.ID, session.DeviceID)
		return
	}

	if err := h.sessions.SetCurrentJob(ctx, session.ID, job.ID); err != nil {
		h.logger.Error("failed to record in-flight job", "err", err, "session_id", session.ID, "job_id", job.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	requestBody, ok := h.renderJobRequest(job)
	if !ok {
		h.logger.Error("job has unrenderable payload; failing it", "job_id", job.ID, "type", job.Type)
		if err := h.jobs.MarkFailed(ctx, job.ID, "", "unrenderable job payload"); err != nil {
			h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
		}
		h.respondEmpty(w)
		return
	}

	h.logger.Info("dispatching job RPC", "job_id", job.ID, "command_key", job.CommandKey,
		"type", job.Type, "device_id", session.DeviceID, "session_id", session.ID)

	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(requestBody)
}

// handleTransferComplete acks the CPE's TransferComplete RPC and
// completes whichever job its CommandKey names — found by CommandKey,
// not by any session state, since this may be a session that never
// dispatched anything itself (build plan §4 Phase 4).
func (h *handler) handleTransferComplete(ctx context.Context, w http.ResponseWriter, tc *cwmp.TransferComplete, respID, ns string) {
	job, err := h.jobs.ByCommandKey(ctx, tc.CommandKey)
	if err != nil {
		h.logger.Warn("TransferComplete for unrecognized command_key", "command_key", tc.CommandKey, "err", err)
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cwmp.RenderTransferCompleteResponseNS(respID, ns))
		return
	}

	// Idempotency (audit P3.3 "delayed/duplicate events"): CPEs retransmit
	// TransferComplete until acked, and some send it again after a reboot.
	// Only a job still waiting on the transfer may be completed by it; a
	// duplicate, or a stale fault arriving after success, is acked and
	// otherwise ignored, so it cannot re-queue confirmations or flip a
	// finished job's outcome.
	if job.Status != jobs.StatusAwaitingTransferComplete && job.Status != jobs.StatusRPCSent {
		h.logger.Info("duplicate or late TransferComplete ignored", "command_key", tc.CommandKey, "job_id", job.ID, "status", job.Status)
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cwmp.RenderTransferCompleteResponseNS(respID, ns))
		return
	}

	if tc.IsFault() {
		if err := h.jobs.MarkFailed(ctx, job.ID, tc.FaultStruct.FaultCode, tc.FaultStruct.FaultString); err != nil {
			h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
		}
		h.auditFailure(ctx, job.DeviceID, job, tc.FaultStruct.FaultCode, tc.FaultStruct.FaultString)
	} else {
		h.markJobSuccess(ctx, job.DeviceID, job)

		// TransferComplete is shared between FIRMWARE_DOWNLOAD and UPLOAD
		// (both are CPE-driven async transfers, TR-069 §9.2/§A.3.2.7) —
		// only firmware has a SoftwareVersion worth confirming afterward
		// (v3 §9.3: don't just trust the ack). An Upload's real
		// confirmation is the file itself landing at the receipt endpoint,
		// handled separately by cmd/api's upload-receive handler.
		if job.Type == jobs.TypeFirmwareDownload {
			confirm, err := h.jobs.Create(ctx, job.DeviceID, jobs.TypeGetParameter,
				jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo.SoftwareVersion"}}, "system:confirm")
			if err != nil {
				h.logger.Error("failed to queue post-firmware version confirmation", "err", err, "device_id", job.DeviceID)
			} else {
				h.metrics.JobsCreatedTotal.WithLabelValues(jobs.TypeGetParameter).Inc()
				h.logger.Info("queued post-firmware version confirmation", "job_id", confirm.ID, "command_key", confirm.CommandKey, "device_id", job.DeviceID)
			}
		}
	}

	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cwmp.RenderTransferCompleteResponseNS(respID, ns))
}

func (h *handler) cacheParameterValues(ctx context.Context, deviceID string, list []cwmp.ParameterValueStruct, source string) {
	if len(list) == 0 {
		return
	}
	values := make(map[string]parameters.CachedValue, len(list))
	now := time.Now().UTC()
	for _, p := range list {
		values[p.Name] = parameters.CachedValue{Value: p.Value, UpdatedAt: now, Source: source}
	}
	if err := h.params.Upsert(ctx, deviceID, values); err != nil {
		h.logger.Error("failed to upsert parameter cache", "err", err, "device_id", deviceID, "source", source)
	}
}

// extractConnectionRequestURL pulls ManagementServer.ConnectionRequestURL
// out of an Inform's ParameterList, checking both data model roots (v3
// §6.1) since data_model_root detection isn't wired up yet — a device
// reporting under either root gets its Connection Request URL captured.
func extractConnectionRequestURL(list []cwmp.ParameterValueStruct) string {
	for _, p := range list {
		switch p.Name {
		case "Device.ManagementServer.ConnectionRequestURL",
			"InternetGatewayDevice.ManagementServer.ConnectionRequestURL":
			return p.Value
		}
	}
	return ""
}

// extractSTUNStatus pulls ManagementServer.UDPConnectionRequestAddress and
// .NATDetected out of an Inform's ParameterList (critical feature backlog:
// STUN NAT traversal) — checking both data model roots, same convention as
// extractConnectionRequestURL. Returns ok=false if the CPE didn't report
// UDPConnectionRequestAddress at all (most CPEs, since it's only populated
// once STUN is enabled and has actually bound), so callers don't overwrite
// a real previous value with an empty one just because one Inform happened
// not to carry it.
func extractSTUNStatus(list []cwmp.ParameterValueStruct) (addr string, natDetected bool, ok bool) {
	for _, p := range list {
		switch p.Name {
		case "Device.ManagementServer.UDPConnectionRequestAddress",
			"InternetGatewayDevice.ManagementServer.UDPConnectionRequestAddress":
			addr = p.Value
			ok = true
		case "Device.ManagementServer.NATDetected",
			"InternetGatewayDevice.ManagementServer.NATDetected":
			natDetected = p.Value == "1" || p.Value == "true"
		}
	}
	return addr, natDetected, ok
}

func (h *handler) closeSession(ctx context.Context, w http.ResponseWriter, sessionID, deviceID string) {
	if err := h.sessions.Close(ctx, sessionID, "NO_PENDING_RPCS"); err != nil {
		h.logger.Error("failed to close session", "err", err, "session_id", sessionID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(ctx, "system", deviceID, "SessionClosed", map[string]any{
		"session_id": sessionID,
		"reason":     "NO_PENDING_RPCS",
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	h.logger.Info("session closed", "session_id", sessionID, "device_id", deviceID, "reason", "NO_PENDING_RPCS")
	h.respondEmpty(w)
}

func (h *handler) respondEmpty(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusOK)
}
