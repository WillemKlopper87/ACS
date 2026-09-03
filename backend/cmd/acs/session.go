// CWMP session handling: the HTTP entry point, Inform, the per-request
// dispatch loop, TransferComplete, and session close (split out of
// main.go, audit P3.1).
package main

import (
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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

// readCWMPBody accepts the encodings seen on real CWMP stacks while
// keeping both the encoded and decoded forms under maxBodyBytes. A 415
// for an unsupported content-coding is intentionally distinct from a
// malformed SOAP 400: compliant CPEs can retry without compression.
// "deflate" is tried as zlib-wrapped RFC 1950 first and then raw RFC 1951
// because both interpretations exist in older embedded HTTP libraries.
func readCWMPBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	encoded, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	if idx := strings.IndexByte(encoding, ','); idx >= 0 {
		// Stacked content-codings are not useful for CWMP and are rare in
		// embedded CPEs. Reject them explicitly rather than decoding in an
		// ambiguous order.
		return nil, &unsupportedEncodingError{encoding: encoding}
	}

	var reader io.Reader = bytes.NewReader(encoded)
	var closer io.Closer
	switch encoding {
	case "", "identity":
	case "gzip", "x-gzip":
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, err
		}
		reader, closer = gz, gz
	case "deflate":
		zr, err := zlib.NewReader(reader)
		if err == nil {
			reader, closer = zr, zr
		} else {
			fr := flate.NewReader(bytes.NewReader(encoded))
			reader, closer = fr, fr
		}
	default:
		return nil, &unsupportedEncodingError{encoding: encoding}
	}
	if closer != nil {
		defer closer.Close()
	}

	decoded, err := io.ReadAll(io.LimitReader(reader, int64(maxBodyBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(decoded) > maxBodyBytes {
		return nil, fmt.Errorf("decoded CWMP body exceeds %d bytes", maxBodyBytes)
	}
	return decoded, nil
}

type unsupportedEncodingError struct{ encoding string }

func (e *unsupportedEncodingError) Error() string {
	return "unsupported Content-Encoding: " + e.encoding
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

	if !h.ipLimiter.Allow(remoteIP(r)) {
		h.metrics.RateLimitRejectedTotal.Inc()
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	if r.URL.Path != "/cwmp" {
		h.logger.Debug("CWMP POST on non-standard path (device ACS URL differs from /cwmp)", "path", r.URL.Path, "remote", r.RemoteAddr)
	}

	authMode := devices.AuthModeNone
	// bound is the device identity this request's credential asserts
	// (audit C-1) — empty fields mean the shared fleet credential was
	// used, which asserts no device identity at all (the pre-existing,
	// documented compatibility tradeoff of a shared secret). A per-device
	// credential or a cert with a CommonName does assert one, and
	// handleInform below must reject any Inform that claims a different
	// device than what actually authenticated it.
	var bound inboundIdentity
	mtlsAuthenticated := r.TLS != nil && len(r.TLS.PeerCertificates) > 0
	if mtlsAuthenticated {
		authMode = devices.AuthModeMTLS
		bound.mtlsNaturalKey = strings.TrimSpace(r.TLS.PeerCertificates[0].Subject.CommonName)
	} else if h.auth.Enabled() {
		ok, stale, identity := h.auth.Verify(r)
		if !ok {
			h.logger.Warn("authentication failed or missing", "remote", r.RemoteAddr)
			// Drain the unauthenticated encoded body before the challenge so
			// keep-alive survives on CPEs that retry the same connection.
			_, _ = io.Copy(io.Discard, r.Body)
			if stale {
				h.auth.ChallengeStale(w)
			} else {
				h.auth.Challenge(w)
			}
			return
		}
		bound.credentialDeviceID = identity.BoundDeviceID
		authMode = devices.AuthModeDigest
	}

	raw, err := readCWMPBody(w, r)
	if err != nil {
		if _, unsupported := err.(*unsupportedEncodingError); unsupported {
			w.Header().Set("Accept-Encoding", "identity, gzip, deflate")
			http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		h.logger.Warn("failed to read/decode request body (possibly oversized)", "err", err, "remote", r.RemoteAddr)
		http.Error(w, "body too large, unreadable, or invalidly compressed", http.StatusBadRequest)
		return
	}

	env, err := cwmp.ParseEnvelope(raw)
	if err != nil {
		h.logger.Error("malformed SOAP/XML", "err", err, "remote", r.RemoteAddr, "body", string(raw))
		http.Error(w, "malformed XML", http.StatusBadRequest)
		return
	}

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
	respID := env.Header.ID
	if respID == "" {
		respID = cwmp.NewID()
	}
	ns := cwmp.DetectCWMPNamespace(raw)

	if env.Body.Inform != nil {
		h.handleInform(ctx, w, r, env.Body.Inform, authMode, bound, respID, ns)
		return
	}

	if env.Body.TransferComplete != nil {
		h.handleTransferComplete(ctx, w, env.Body.TransferComplete, bound, respID, ns)
		return
	}

	h.dispatch(ctx, w, r, env.Body)
}

// inboundIdentity is what this request's credential asserts about device
// identity (audit C-1). At most one field is ever set: mtlsNaturalKey for
// an mTLS-authenticated request whose leaf certificate's CommonName names
// a specific device (by convention, that device's oui_serial natural
// key), credentialDeviceID for a per-device Digest/Basic credential.
// Both empty means the shared fleet credential authenticated the
// request, which — same as today — asserts no device identity to bind.
type inboundIdentity struct {
	credentialDeviceID string
	mtlsNaturalKey     string
}

func (h *handler) handleInform(ctx context.Context, w http.ResponseWriter, r *http.Request, inform *cwmp.Inform, authMode string, bound inboundIdentity, respID, ns string) {
	h.metrics.InformsTotal.Inc()
	events := inform.EventCodes()

	if inform.DeviceId.OUI == "" || inform.DeviceId.SerialNumber == "" {
		h.logger.Warn("Inform rejected: OUI and SerialNumber are required for a device identity", "remote", r.RemoteAddr)
		http.Error(w, "Inform DeviceId requires OUI and SerialNumber", http.StatusBadRequest)
		return
	}
	naturalKey := inform.DeviceId.NaturalKey()

	// audit C-1: a credential bound to one device must not be able to
	// author another device's Inform — that would let its holder receive
	// the victim device's queued jobs (including parameter writes with
	// secrets), mark its jobs complete/failed, or get auto-provisioning
	// templates pushed to it under a forged manufacturer/model match.
	if bound.credentialDeviceID != "" {
		existing, err := h.devices.GetByOUIserial(ctx, naturalKey)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			h.logger.Error("failed to resolve device for identity binding check", "err", err, "natural_key", naturalKey)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err != nil || existing.ID != bound.credentialDeviceID {
			h.logger.Warn("Inform rejected: per-device credential does not match the claimed device identity",
				"bound_device_id", bound.credentialDeviceID, "claimed_natural_key", naturalKey, "remote", r.RemoteAddr)
			http.Error(w, "device identity mismatch", http.StatusForbidden)
			return
		}
	}
	if bound.mtlsNaturalKey != "" && bound.mtlsNaturalKey != naturalKey {
		h.logger.Warn("Inform rejected: mTLS certificate CommonName does not match the claimed device identity",
			"cert_cn", bound.mtlsNaturalKey, "claimed_natural_key", naturalKey, "remote", r.RemoteAddr)
		http.Error(w, "device identity mismatch", http.StatusForbidden)
		return
	}

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

	session, err := h.sessions.Open(ctx, device.ID, events, ns)
	if err != nil {
		h.logger.Error("failed to open session", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.metrics.SessionsOpenedTotal.Inc()

	if err := h.auditor.Record(ctx, "system", device.ID, "Inform", map[string]any{
		"event_codes":    events,
		"session_id":     session.ID,
		"cwmp_namespace": ns,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	h.logger.Info("Inform received",
		"device_id", device.ID,
		"oui_serial", device.OUISerial,
		"manufacturer", device.Manufacturer,
		"events", events,
		"session_id", session.ID,
		"cwmp_namespace", ns,
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
	requestBody = cwmp.RewriteCWMPNamespace(requestBody, session.CWMPNamespace)

	h.logger.Info("dispatching job RPC", "job_id", job.ID, "command_key", job.CommandKey,
		"type", job.Type, "device_id", session.DeviceID, "session_id", session.ID,
		"cwmp_namespace", session.CWMPNamespace)

	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(requestBody)
}

// handleTransferComplete acks the CPE's TransferComplete RPC and
// completes whichever job its CommandKey names — found by CommandKey,
// not by any session state, since this may be a session that never
// dispatched anything itself (build plan §4 Phase 4).
//
// audit P1.1/M-2: CommandKey alone identifies a job but does not prove the
// sender is the job's device — CommandKeys are 32-bit and appear in REST
// responses, audit rows and logs, so any authenticated CPE that learns
// another device's CommandKey could otherwise mark that transfer
// FAILED, which (via rollout.FailureRate) can force a fleet-wide
// rollback. bound is the identity this specific request's credential
// actually authenticated (audit C-1, resolved in handleCWMP); when it
// asserts a device, that device must equal job.DeviceID.
func (h *handler) handleTransferComplete(ctx context.Context, w http.ResponseWriter, tc *cwmp.TransferComplete, bound inboundIdentity, respID, ns string) {
	job, err := h.jobs.ByCommandKey(ctx, tc.CommandKey)
	if err != nil {
		h.logger.Warn("TransferComplete for unrecognized command_key", "command_key", tc.CommandKey, "err", err)
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cwmp.RenderTransferCompleteResponseNS(respID, ns))
		return
	}

	if bound.credentialDeviceID != "" && bound.credentialDeviceID != job.DeviceID {
		h.logger.Warn("TransferComplete rejected: authenticated credential is bound to a different device than the job",
			"command_key", tc.CommandKey, "job_id", job.ID, "job_device_id", job.DeviceID, "bound_device_id", bound.credentialDeviceID)
		http.Error(w, "device identity mismatch", http.StatusForbidden)
		return
	}
	if bound.mtlsNaturalKey != "" {
		device, err := h.devices.Get(ctx, job.DeviceID)
		if err != nil || device.OUISerial != bound.mtlsNaturalKey {
			h.logger.Warn("TransferComplete rejected: mTLS certificate does not match the job's device",
				"command_key", tc.CommandKey, "job_id", job.ID, "job_device_id", job.DeviceID, "cert_cn", bound.mtlsNaturalKey)
			http.Error(w, "device identity mismatch", http.StatusForbidden)
			return
		}
	}

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

func extractSTUNStatus(list []cwmp.ParameterValueStruct) (addr string, natDetected bool, ok bool) {
	for _, p := range list {
		switch p.Name {
		case "Device.ManagementServer.UDPConnectionRequestAddress",
			"InternetGatewayDevice.ManagementServer.UDPConnectionRequestAddress":
			addr = p.Value
			ok = true
		case "Device.ManagementServer.NATDetected",
			"InternetGatewayDevice.ManagementServer.NATDetected":
			natDetected = p.Value == "1" || strings.EqualFold(p.Value, "true")
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
	// TR-069's normal no-more-RPCs response is HTTP 204 with no entity.
	// A compatibility escape hatch retains the old HTTP 200-empty
	// behavior for a known-broken vendor without making it the fleet-wide
	// default: ACS_CWMP_EMPTY_RESPONSE_STATUS=200.
	if envOr("ACS_CWMP_EMPTY_RESPONSE_STATUS", "204") == "200" {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
