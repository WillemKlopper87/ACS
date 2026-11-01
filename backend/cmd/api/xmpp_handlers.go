// XMPP Connection Request REST surface (TR-069 Annex K): start zero-touch
// provisioning and read a device's XMPP state. Like credential_handlers.go,
// no password ever appears in a response, a log line or an audit record.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"acs/internal/credentials"
	"acs/internal/devices"
	"acs/internal/xmpp"
	"acs/internal/xmppprov"
)

type xmppProvisioningResponse struct {
	State                 string  `json:"state"`
	ConnectionPath        string  `json:"connection_path,omitempty"`
	ServerPath            string  `json:"server_path,omitempty"`
	CredentialID          string  `json:"credential_id,omitempty"`
	JobID                 string  `json:"job_id,omitempty"`
	Attempts              int     `json:"attempts"`
	Domain                string  `json:"domain"`
	Resource              string  `json:"resource"`
	ServerAddress         string  `json:"server_address"`
	ServerPort            int     `json:"server_port"`
	KeepAliveInterval     int     `json:"keepalive_interval"`
	ACSJID                string  `json:"acs_jid"`
	RelaxCertVerification bool    `json:"relax_certificate_verification"`
	BypassHostnameCheck   bool    `json:"bypass_hostname_check"`
	JabberID              string  `json:"jabber_id,omitempty"`
	ConnectionStatus      string  `json:"connection_status,omitempty"`
	LastError             string  `json:"last_error,omitempty"`
	RequestedBy           string  `json:"requested_by,omitempty"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
	CompletedAt           *string `json:"completed_at,omitempty"`
}

func toXMPPProvisioningResponse(p *xmppprov.Provisioning) *xmppProvisioningResponse {
	if p == nil {
		return nil
	}
	resp := &xmppProvisioningResponse{
		State: p.State, ConnectionPath: p.ConnectionPath, ServerPath: p.ServerPath, CredentialID: p.CredentialID,
		JobID: p.JobID, Attempts: p.Attempts, Domain: p.Domain, Resource: p.Resource, ServerAddress: p.ServerAddress,
		ServerPort: p.ServerPort, KeepAliveInterval: p.KeepAliveInterval, ACSJID: p.ACSJID,
		RelaxCertVerification: p.RelaxCertVerification, BypassHostnameCheck: p.BypassHostnameCheck,
		JabberID: p.JabberID, ConnectionStatus: p.ConnectionStatus, LastError: p.LastError, RequestedBy: p.RequestedBy,
		CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.CompletedAt != nil {
		s := p.CompletedAt.UTC().Format(time.RFC3339)
		resp.CompletedAt = &s
	}
	return resp
}

type provisionXMPPRequest struct {
	// RelaxCertificateVerification sets the vendor's XMPP
	// CertificateVerificationRequired to false — only for a lab server with
	// a self-signed certificate the CPE cannot be given a CA for.
	RelaxCertificateVerification bool `json:"relax_certificate_verification"`
	// BypassHostnameCheck sets the vendor's BypassHostnameCheck — for a
	// server reached by IP whose certificate names the XMPP domain.
	BypassHostnameCheck bool `json:"bypass_hostname_check"`
	// Restart re-provisions a device whose last run finished (DONE or
	// FAILED) with a fresh XMPP credential, reusing its XMPP Connection
	// instance when one was created before.
	Restart bool `json:"restart"`
}

// provisionXMPP starts zero-touch XMPP provisioning (see
// internal/xmppprov for the state machine). It is idempotent: a run in
// progress is returned as-is (202), a finished one too (200) unless
// restart is set.
func (h *handler) provisionXMPP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}
	var req provisionXMPPRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}
	if h.xmpp == nil {
		http.Error(w, "XMPP Connection Request is not configured on this ACS (ACS_XMPP_SERVER)", http.StatusServiceUnavailable)
		return
	}
	if h.xmpp.cpeServerAddress == "" {
		http.Error(w, "ACS_XMPP_CPE_SERVER_ADDRESS is not set — the ACS does not know which address CPEs should connect to", http.StatusConflict)
		return
	}

	existing, err := h.xmppProvisioning.Get(r.Context(), id)
	if err != nil && !errors.Is(err, xmppprov.ErrNotFound) {
		h.logger.Error("failed to read xmpp provisioning", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if existing != nil && !xmppprov.Terminal(existing.State) {
		writeJSON(w, http.StatusAccepted, map[string]any{"existing": true, "provisioning": toXMPPProvisioningResponse(existing)})
		return
	}
	if existing != nil && !req.Restart {
		writeJSON(w, http.StatusOK, map[string]any{"existing": true, "provisioning": toXMPPProvisioningResponse(existing)})
		return
	}

	if device.DataModelRoot != devices.DataModelRootDevice2 {
		http.Error(w, "XMPP provisioning needs a discovered TR-181 (Device:2) data model — Device.XMPP. does not exist in TR-098", http.StatusConflict)
		return
	}
	discovered, err := h.params.GetNames(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to read discovered parameters", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if discovered == nil || !discovered.Names[xmppprov.ConnectionTable] {
		http.Error(w, "parameter discovery has not found a writable "+xmppprov.ConnectionTable+" table on this device; run parameter discovery first", http.StatusConflict)
		return
	}
	if cached, err := h.params.Get(r.Context(), id); err == nil {
		if methods := cached[xmppprov.ManagementServer+"SupportedConnReqMethods"].Value; methods != "" && !containsToken(methods, "XMPP") {
			http.Error(w, "device reports SupportedConnReqMethods="+methods+" (no XMPP)", http.StatusConflict)
			return
		}
	}
	if !h.xmpp.client.Connected() {
		http.Error(w, "the ACS is not connected to its XMPP server right now; check the server and retry", http.StatusServiceUnavailable)
		return
	}

	username, password, err := credentials.GenerateXMPPCredential()
	if err != nil {
		h.logger.Error("failed to generate xmpp credential", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cred, err := h.credentials.Create(r.Context(), id, credentials.TypeXMPP, username, password, "")
	if err != nil {
		h.logger.Error("failed to record xmpp credential", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	accountCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	err = h.xmpp.client.EnsureAccount(accountCtx, username+"@"+h.xmpp.client.Domain(), password)
	cancel()
	if err != nil {
		_, _ = h.credentials.Revoke(r.Context(), cred.ID)
		h.logger.Error("failed to create the device's XMPP account", "err", err, "device_id", id)
		http.Error(w, "failed to create the device's XMPP account on the XMPP server: "+err.Error(), http.StatusBadGateway)
		return
	}

	p := &xmppprov.Provisioning{
		DeviceID: id, State: xmppprov.StateAddConnection, CredentialID: cred.ID,
		Domain: h.xmpp.client.Domain(), Resource: h.xmpp.cpeResource,
		ServerAddress: h.xmpp.cpeServerAddress, ServerPort: h.xmpp.cpeServerPort, KeepAliveInterval: h.xmpp.cpeKeepAlive,
		ACSJID: h.xmpp.client.BareJID(), RelaxCertVerification: req.RelaxCertificateVerification,
		BypassHostnameCheck: req.BypassHostnameCheck, RequestedBy: operatorFromRequest(r),
	}
	reused := false
	if existing != nil && existing.ConnectionPath != "" {
		if _, ok := discovered.Names[existing.ConnectionPath]; ok {
			// Re-provisioning: rewrite the instance created last time
			// instead of adding a second XMPP connection to the CPE.
			p.ConnectionPath, p.State, reused = existing.ConnectionPath, xmppprov.StateDiscoverConnection, true
		}
	}
	if err := h.xmppProvisioning.Start(r.Context(), p); errors.Is(err, xmppprov.ErrActive) {
		_, _ = h.credentials.Revoke(r.Context(), cred.ID)
		current, _ := h.xmppProvisioning.Get(r.Context(), id)
		writeJSON(w, http.StatusAccepted, map[string]any{"existing": true, "provisioning": toXMPPProvisioningResponse(current)})
		return
	} else if err != nil {
		_, _ = h.credentials.Revoke(r.Context(), cred.ID)
		h.logger.Error("failed to start xmpp provisioning", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// An abandoned earlier attempt's credential is never going to be
	// activated; revoke it so only live accounts remain listed.
	if existing != nil && existing.CredentialID != "" && existing.CredentialID != cred.ID {
		if old, err := h.credentials.ByID(r.Context(), existing.CredentialID); err == nil && old.Status == credentials.StatusPending {
			_, _ = h.credentials.Revoke(r.Context(), old.ID)
		}
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), id, "XMPPProvisioning", map[string]any{
		"phase": "started", "credential_id": cred.ID, "username": "***", "password": "***",
		"relax_certificate_verification": req.RelaxCertificateVerification, "bypass_hostname_check": req.BypassHostnameCheck,
		"server_address": p.ServerAddress, "server_port": p.ServerPort, "reused_connection": reused,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	started, err := h.xmppProvisioning.Get(r.Context(), id)
	if err != nil {
		started = p
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"existing": false, "provisioning": toXMPPProvisioningResponse(started)})
}

func containsToken(list, token string) bool {
	for _, t := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(t), token) {
			return true
		}
	}
	return false
}

// getDeviceXMPP reports a device's XMPP Connection Request state: the
// ACS's own session, what the CPE reports, and provisioning progress.
func (h *handler) getDeviceXMPP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}
	acs := map[string]any{"configured": h.xmpp != nil}
	if h.xmpp != nil {
		st := h.xmpp.client.Status()
		acs["connected"] = st.Connected
		acs["jid"] = st.JID
		acs["connected_since"] = st.ConnectedSince
		acs["last_error"] = st.LastError
		acs["cpe_server_address"] = h.xmpp.cpeServerAddress
		acs["cpe_server_port"] = h.xmpp.cpeServerPort
	}

	cached, err := h.params.Get(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to read parameter cache", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	prov, err := h.xmppProvisioning.Get(r.Context(), id)
	if err != nil && !errors.Is(err, xmppprov.ErrNotFound) {
		h.logger.Error("failed to read xmpp provisioning", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	jid := cached[connReqJabberIDParam(device.DataModelRoot)].Value
	cpe := map[string]any{
		"conn_req_jabber_id":         jid,
		"conn_req_jabber_id_valid":   jid != "" && xmpp.ValidateFullJID(jid) == nil,
		"supported_conn_req_methods": cached[xmppprov.ManagementServer+"SupportedConnReqMethods"].Value,
		"conn_req_xmpp_connection":   cached[xmppprov.ManagementServer+"ConnReqXMPPConnection"].Value,
		"connection_request_mode":    device.ConnectionRequestMode,
	}
	if prov != nil && prov.ConnectionPath != "" {
		cpe["xmpp_connection_status"] = cached[prov.ConnectionPath+"Status"].Value
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id": id, "acs": acs, "cpe": cpe, "provisioning": toXMPPProvisioningResponse(prov), "as_of": time.Now().UTC(),
	})
}
