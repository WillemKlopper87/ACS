package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"acs/internal/connreq"
	"acs/internal/credentials"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/observability"
)

const (
	connReqPollInterval = 1 * time.Second        // how often the worker checks for new QUEUED CONNECTION_REQUEST jobs
	connReqWaitPoll     = 500 * time.Millisecond // how often it checks whether the CPE has re-Informed
	connReqGETTimeout   = 10 * time.Second       // v3 §5.6 pseudocode's own default
	connReqDefaultWait  = 30 * time.Second
	connReqMaxWait      = 120 * time.Second
)

// connectionRequestWorker is the background loop that turns queued
// CONNECTION_REQUEST jobs into actual outbound GETs to CPEs (build plan
// §4 Phase 3). It exists because, unlike SET_PARAMETER/GET_PARAMETER,
// nothing about receiving a CWMP request triggers this job type — the
// worker has to go looking for the work instead of having it handed to
// it by cmd/acs's session dispatch.
type connectionRequestWorker struct {
	logger      *slog.Logger
	jobs        *jobs.Repository
	devices     *devices.Repository
	credentials *credentials.Repository
	auditor     *observability.Auditor
	username    string // fallback shared credential (Phase 3) for devices with no rotated credential (Phase 6, §11.6)
	password    string
}

// credentialFor resolves which username/password to use for this
// device's Connection Request GET: a per-device ACTIVE credential if
// design doc v3 §11.6's rotation flow has ever run for it, otherwise the
// shared fallback every device used before Phase 6. This is the "switch
// the ACS's Connection Request client to the new credential" step —
// implemented as "look it up fresh every time" rather than an in-memory
// client object, since there is no long-lived client here to mutate.
func (w *connectionRequestWorker) credentialFor(ctx context.Context, deviceID string) (username, password string) {
	if w.credentials == nil {
		return w.username, w.password
	}
	cred, err := w.credentials.ActiveForDevice(ctx, deviceID, credentials.TypeConnectionRequest)
	if err != nil {
		return w.username, w.password
	}
	return cred.Username, cred.Password
}

func (w *connectionRequestWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(connReqPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, err := w.jobs.LeaseNextByType(ctx, jobs.TypeConnectionRequest)
			if err != nil {
				w.logger.Error("failed to lease connection request job", "err", err)
				continue
			}
			if job == nil {
				continue
			}
			go w.process(ctx, job)
		}
	}
}

func (w *connectionRequestWorker) process(ctx context.Context, job *jobs.Job) {
	var payload jobs.ConnectionRequestPayload
	_ = json.Unmarshal(job.Payload, &payload) // zero-value TimeoutSeconds is fine, clamped below

	wait := time.Duration(payload.TimeoutSeconds) * time.Second
	if wait <= 0 {
		wait = connReqDefaultWait
	}
	if wait > connReqMaxWait {
		wait = connReqMaxWait
	}

	device, err := w.devices.Get(ctx, job.DeviceID)
	if err != nil {
		w.logger.Error("failed to load device for connection request", "err", err, "job_id", job.ID)
		_ = w.jobs.MarkFailed(ctx, job.ID, "", "failed to load device")
		return
	}

	username, password := w.credentialFor(ctx, device.ID)
	udpAddr := ""
	if device.UDPConnectionRequestAddress != nil {
		udpAddr = *device.UDPConnectionRequestAddress
	}

	if device.ConnectionRequestURL == nil || *device.ConnectionRequestURL == "" {
		if udpAddr != "" {
			// No TCP path at all, but STUN gave us a reflexive address —
			// Annex G is the only option.
			w.attemptAnnexG(ctx, device, job, udpAddr, username, password, wait)
			return
		}
		w.logger.Warn("connection request has no URL to call", "device_id", device.ID, "job_id", job.ID)
		_ = w.jobs.MarkFailed(ctx, job.ID, "", connreq.OutcomeUnavailable)
		_ = w.devices.RecordConnectionRequestAttempt(ctx, device.ID, connreq.OutcomeUnavailable, "")
		w.audit(ctx, device.ID, job, connreq.OutcomeUnavailable)
		return
	}

	attemptedAt := time.Now().UTC()
	getCtx, cancel := context.WithTimeout(ctx, connReqGETTimeout)
	outcome := connreq.Attempt(getCtx, *device.ConnectionRequestURL, username, password, connReqGETTimeout)
	cancel()

	w.logger.Info("connection request GET completed", "device_id", device.ID, "job_id", job.ID, "outcome", outcome)

	if outcome != connreq.OutcomeHTTP200 {
		if udpAddr != "" {
			// The direct GET didn't get through (CGNAT, firewall, stale
			// URL) but the device advertised a STUN-learned UDP address:
			// fall through to a TR-069 Annex G UDP Connection Request.
			w.logger.Info("direct connection request failed; trying Annex G UDP", "device_id", device.ID, "job_id", job.ID, "http_outcome", outcome, "udp_addr", udpAddr)
			w.attemptAnnexG(ctx, device, job, udpAddr, username, password, wait)
			return
		}
		_ = w.devices.RecordConnectionRequestAttempt(ctx, device.ID, outcome, "")
		_ = w.jobs.MarkFailed(ctx, job.ID, "", outcome)
		w.audit(ctx, device.ID, job, outcome)
		return
	}

	// v3 §5.6: "A successful Connection Request HTTP 200 does not
	// necessarily mean the CPE session has opened yet" — wait and see.
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		informed, err := w.devices.InformedWithEventSince(ctx, device.ID, attemptedAt, "6")
		if err != nil {
			w.logger.Error("failed to check for post-connection-request inform", "err", err, "device_id", device.ID)
		} else if informed {
			_ = w.devices.RecordConnectionRequestAttempt(ctx, device.ID, "HTTP_200_INFORM_RECEIVED", devices.ModeDirectIPv4)
			_ = w.jobs.MarkSuccess(ctx, job.ID)
			w.logger.Info("connection request succeeded", "device_id", device.ID, "job_id", job.ID)
			w.audit(ctx, device.ID, job, "HTTP_200_INFORM_RECEIVED")
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(connReqWaitPoll):
		}
	}

	// GET succeeded but no Inform arrived — the CGNAT case v3 §12.4 calls
	// out specifically: the device is reachable enough to accept the wake
	// request, but can't (or didn't) open a return session. Downgrade the
	// reachability mode rather than leaving it as a fluke.
	_ = w.devices.RecordConnectionRequestAttempt(ctx, device.ID, "HTTP_200_NO_INFORM", devices.ModePeriodicFallback)
	_ = w.jobs.MarkTimeout(ctx, job.ID, "HTTP_200_NO_INFORM: no Inform with EventCode 6 within the wait window")
	w.logger.Warn("connection request timed out waiting for inform", "device_id", device.ID, "job_id", job.ID)
	w.audit(ctx, device.ID, job, "HTTP_200_NO_INFORM")
}

// attemptAnnexG sends the Annex G UDP wake-up (internal/connreq.SendUDP)
// and waits for the CPE's EventCode 6 Inform, the only delivery signal
// UDP offers. Success records ModeSTUNAnnexG so later requests know this
// device is reached that way.
func (w *connectionRequestWorker) attemptAnnexG(ctx context.Context, device *devices.Device, job *jobs.Job, udpAddr, username, password string, wait time.Duration) {
	attemptedAt := time.Now().UTC()
	sendCtx, cancel := context.WithTimeout(ctx, connReqGETTimeout)
	err := connreq.SendUDP(sendCtx, udpAddr, username, password)
	cancel()
	if err != nil {
		w.logger.Warn("annex g udp connection request failed to send", "err", err, "device_id", device.ID, "job_id", job.ID)
		_ = w.devices.RecordConnectionRequestAttempt(ctx, device.ID, connreq.OutcomeUDPSendFailed, "")
		_ = w.jobs.MarkFailed(ctx, job.ID, "", connreq.OutcomeUDPSendFailed)
		w.audit(ctx, device.ID, job, connreq.OutcomeUDPSendFailed)
		return
	}
	w.logger.Info("annex g udp connection request sent", "device_id", device.ID, "job_id", job.ID, "udp_addr", udpAddr)

	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		informed, err := w.devices.InformedWithEventSince(ctx, device.ID, attemptedAt, "6")
		if err != nil {
			w.logger.Error("failed to check for post-connection-request inform", "err", err, "device_id", device.ID)
		} else if informed {
			_ = w.devices.RecordConnectionRequestAttempt(ctx, device.ID, connreq.OutcomeUDPInformReceived, devices.ModeSTUNAnnexG)
			_ = w.jobs.MarkSuccess(ctx, job.ID)
			w.logger.Info("annex g connection request succeeded", "device_id", device.ID, "job_id", job.ID)
			w.audit(ctx, device.ID, job, connreq.OutcomeUDPInformReceived)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(connReqWaitPoll):
		}
	}
	_ = w.devices.RecordConnectionRequestAttempt(ctx, device.ID, connreq.OutcomeUDPNoInform, devices.ModePeriodicFallback)
	_ = w.jobs.MarkTimeout(ctx, job.ID, connreq.OutcomeUDPNoInform+": no Inform with EventCode 6 within the wait window after the UDP wake-up")
	w.logger.Warn("annex g connection request timed out waiting for inform", "device_id", device.ID, "job_id", job.ID)
	w.audit(ctx, device.ID, job, connreq.OutcomeUDPNoInform)
}

// audit records the ConnectionRequest action (design doc v3 §11.8 lists
// it explicitly, alongside SetParameterValues/Download/Reboot/... — every
// write-shaped action the ACS performs gets an audit entry).
func (w *connectionRequestWorker) audit(ctx context.Context, deviceID string, job *jobs.Job, outcome string) {
	if err := w.auditor.Record(ctx, "system", deviceID, "ConnectionRequest", map[string]any{
		"job_id": job.ID, "command_key": job.CommandKey, "outcome": outcome,
	}); err != nil {
		w.logger.Error("failed to write audit record", "err", err)
	}
}
