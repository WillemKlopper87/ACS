// XMPP Connection Request (TR-069 Annex K) for the Connection Request
// worker. A CGNAT'd CPE (field test 2026-10-30: Nokia FastMile 5G Gateway 2,
// ConnectionRequestURL on a private carrier address, no STUN) cannot be
// dialled, but once provisioned (xmpp_provisioner.go) it holds an outbound
// session to the ACS's XMPP server; a connectionRequest iq sent there makes
// it open a CWMP session within seconds instead of at its next periodic
// Inform.
package main

import (
	"context"
	"errors"
	"time"

	"acs/internal/connreq"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/xmpp"
)

// connReqXMPPTimeout bounds the wait for the CPE's iq answer. It answers
// before it opens the CWMP session, so a few seconds is plenty; the same
// 10 s the HTTP GET gets keeps the worst case symmetrical.
const connReqXMPPTimeout = 10 * time.Second

// xmppConnectionRequester is the slice of *xmpp.Client the worker needs
// (an interface so path selection and outcomes are testable without a
// server).
type xmppConnectionRequester interface {
	Connected() bool
	ConnectionRequest(ctx context.Context, to, username, password string) error
}

// Connection Request paths, in the order the worker considers them.
const (
	connReqPathXMPP        = "XMPP"
	connReqPathAnnexG      = "ANNEX_G"
	connReqPathUnavailable = "UNAVAILABLE"
	connReqPathBehindNAT   = "BEHIND_NAT"
	connReqPathHTTP        = "HTTP"
)

// connReqInputs is everything path selection depends on.
type connReqInputs struct {
	HasURL         bool
	UDPAddr        string
	Mode           string
	BehindNAT      bool   // Inform-time NAT judgement (only consulted for XMPP-mode devices)
	JabberID       string // validated full JID from the cached ConnReqJabberID, or ""
	XMPPConfigured bool   // ACS_XMPP_* set; false again after an XMPP attempt failed
}

// selectConnReqPath decides how to reach a device. XMPP goes first whenever
// the ACS has XMPP and the CPE has reported a JID: it needs no inbound path,
// so a NAT'd device never costs a 10 s HTTP dial. Without XMPP the
// pre-existing order applies unchanged — no URL means Annex G or nothing,
// a NAT'd device without STUN is skipped (BEHIND_NAT), otherwise direct
// HTTP (falling back to Annex G inside that path). An XMPP-mode device is
// treated like PERIODIC_FALLBACK_ONLY for the skip when the last Inform
// judged it NAT'd: XMPP failing does not make it dialable.
func selectConnReqPath(in connReqInputs) string {
	if in.XMPPConfigured && in.JabberID != "" {
		return connReqPathXMPP
	}
	if !in.HasURL {
		if in.UDPAddr != "" {
			return connReqPathAnnexG
		}
		return connReqPathUnavailable
	}
	if in.UDPAddr == "" && (in.Mode == devices.ModePeriodicFallback || (in.Mode == devices.ModeXMPP && in.BehindNAT)) {
		return connReqPathBehindNAT
	}
	return connReqPathHTTP
}

// connReqJabberIDParam returns the cached ManagementServer.ConnReqJabberID
// path for the device's data model root.
func connReqJabberIDParam(dataModelRoot string) string {
	if dataModelRoot == devices.DataModelRootIGD1 {
		return "InternetGatewayDevice.ManagementServer.ConnReqJabberID"
	}
	return "Device.ManagementServer.ConnReqJabberID"
}

// connReqInputsFor gathers selectConnReqPath's inputs for one device.
func (w *connectionRequestWorker) connReqInputsFor(ctx context.Context, device *devices.Device, udpAddr string) connReqInputs {
	in := connReqInputs{
		HasURL:         device.ConnectionRequestURL != nil && *device.ConnectionRequestURL != "",
		UDPAddr:        udpAddr,
		Mode:           device.ConnectionRequestMode,
		XMPPConfigured: w.xmpp != nil,
	}
	if in.XMPPConfigured && w.params != nil {
		if cached, err := w.params.Get(ctx, device.ID); err == nil {
			jid := cached[connReqJabberIDParam(device.DataModelRoot)].Value
			if jid != "" && xmpp.ValidateFullJID(jid) == nil {
				in.JabberID = jid
			} else if jid != "" {
				w.logger.Warn("ignoring malformed cached ConnReqJabberID", "device_id", device.ID)
			}
		}
	}
	if in.Mode == devices.ModeXMPP {
		if behind, err := w.devices.ConnectionRequestBehindNAT(ctx, device.ID); err == nil {
			in.BehindNAT = behind
		}
	}
	return in
}

// attemptXMPP sends the Annex K Connection Request and, if the CPE
// accepts it, waits for the "6 CONNECTION REQUEST" Inform exactly like the
// HTTP and Annex G paths do. It reports handled=true when the job has been
// completed (success or a post-acknowledgement timeout); false means the
// XMPP attempt failed before the CPE accepted it, and the caller falls back
// to the pre-existing paths. The failure is audited either way so the
// operator can see that XMPP was tried.
func (w *connectionRequestWorker) attemptXMPP(ctx context.Context, device *devices.Device, job *jobs.Job, jid, username, password string, wait time.Duration) (handled bool) {
	if !w.xmpp.Connected() {
		w.logger.Warn("xmpp connection request skipped: ACS has no XMPP session; falling back", "device_id", device.ID, "job_id", job.ID)
		w.auditXMPPFallback(ctx, device.ID, job, connreq.OutcomeXMPPUnavailable, "")
		return false
	}

	attemptedAt := time.Now().UTC()
	sendCtx, cancel := context.WithTimeout(ctx, connReqXMPPTimeout)
	err := w.xmpp.ConnectionRequest(sendCtx, jid, username, password)
	cancel()
	if err != nil {
		outcome, condition := connreq.OutcomeXMPPError, "timeout"
		var se *xmpp.StanzaError
		switch {
		case errors.Is(err, xmpp.ErrNotConnected):
			outcome, condition = connreq.OutcomeXMPPUnavailable, ""
		case errors.As(err, &se):
			condition = se.Condition
		case !errors.Is(err, context.DeadlineExceeded):
			condition = "send_failed"
		}
		w.logger.Warn("xmpp connection request failed; falling back", "device_id", device.ID, "job_id", job.ID,
			"jid", jid, "outcome", outcome, "condition", condition)
		w.auditXMPPFallback(ctx, device.ID, job, outcome, condition)
		return false
	}
	w.logger.Info("xmpp connection request acknowledged by CPE", "device_id", device.ID, "job_id", job.ID, "jid", jid)

	if !w.waitForConnReqInform(ctx, device.ID, attemptedAt, wait) {
		if ctx.Err() != nil {
			return true
		}
		// The CPE took the request but opened no session: do not fall back
		// (it was reachable — dialling HTTP would not change its mind) and
		// do not downgrade the mode; one missed session is not evidence
		// that XMPP stopped working.
		_ = w.devices.RecordConnectionRequestAttempt(ctx, device.ID, connreq.OutcomeXMPPNoInform, "")
		_ = w.jobs.MarkTimeout(ctx, job.ID, connreq.OutcomeXMPPNoInform+": no Inform with EventCode 6 within the wait window after the XMPP connection request")
		w.logger.Warn("xmpp connection request timed out waiting for inform", "device_id", device.ID, "job_id", job.ID)
		w.audit(ctx, device.ID, job, connreq.OutcomeXMPPNoInform)
		return true
	}
	_ = w.devices.RecordConnectionRequestAttempt(ctx, device.ID, connreq.OutcomeXMPPInformReceived, devices.ModeXMPP)
	_ = w.jobs.MarkSuccess(ctx, job.ID)
	w.logger.Info("xmpp connection request succeeded", "device_id", device.ID, "job_id", job.ID, "elapsed", time.Since(attemptedAt).String())
	w.audit(ctx, device.ID, job, connreq.OutcomeXMPPInformReceived)
	return true
}

// waitForConnReqInform polls for an Inform carrying event code 6 since the
// attempt. false on timeout or cancellation (check ctx.Err to tell apart).
func (w *connectionRequestWorker) waitForConnReqInform(ctx context.Context, deviceID string, since time.Time, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		informed, err := w.devices.InformedWithEventSince(ctx, deviceID, since, "6")
		if err != nil {
			w.logger.Error("failed to check for post-connection-request inform", "err", err, "device_id", deviceID)
		} else if informed {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(connReqWaitPoll):
		}
	}
	return false
}

// auditXMPPFallback records an XMPP attempt that did not reach the CPE.
// The job itself continues on the fallback path, which writes its own
// ConnectionRequest audit entry with the final outcome.
func (w *connectionRequestWorker) auditXMPPFallback(ctx context.Context, deviceID string, job *jobs.Job, outcome, condition string) {
	detail := map[string]any{"job_id": job.ID, "command_key": job.CommandKey, "outcome": outcome, "transport": "XMPP", "fallback": true}
	if condition != "" {
		detail["condition"] = condition
	}
	if err := w.auditor.Record(ctx, "system", deviceID, "ConnectionRequest", detail); err != nil {
		w.logger.Error("failed to write audit record", "err", err)
	}
}
