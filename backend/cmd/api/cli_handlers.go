// SSH/Telnet device console (admin-platform backlog: scaffolded per the
// user's explicit "build now, functional later" call, 2026-08-06 — see
// internal/cliaccess's doc comment for the CGNAT reachability caveat this
// carries until a VPN/tunnel path exists).
package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"golang.org/x/net/websocket"

	"acs/internal/cliaccess"
)

type createCLICredentialRequest struct {
	Protocol string `json:"protocol"` // "SSH" or "TELNET"
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type cliCredentialResponse struct {
	ID        string `json:"id"`
	DeviceID  string `json:"device_id"`
	Protocol  string `json:"protocol"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"` // password is deliberately never returned, same rule as internal/credentials
	CreatedAt string `json:"created_at"`
}

func toCLICredentialResponse(c cliaccess.Credential) cliCredentialResponse {
	return cliCredentialResponse{
		ID: c.ID, DeviceID: c.DeviceID, Protocol: c.Protocol, Host: c.Host, Port: c.Port,
		Username: c.Username, CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *handler) createCLICredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	var req createCLICredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Protocol != cliaccess.ProtocolSSH && req.Protocol != cliaccess.ProtocolTelnet {
		http.Error(w, `protocol must be "SSH" or "TELNET"`, http.StatusBadRequest)
		return
	}
	if req.Host == "" || req.Port == 0 || req.Username == "" {
		http.Error(w, "host, port, and username are required", http.StatusBadRequest)
		return
	}
	// audit H-4: validate at save time too, not only at connect — a
	// credential pointed at a disallowed host should be rejected before
	// it's ever stored, the same way webgui_handlers.go validates both.
	if err := h.netPolicy.CheckHost(r.Context(), req.Host); err != nil {
		http.Error(w, "target host is not allowed: "+err.Error(), http.StatusForbidden)
		return
	}

	cred, err := h.cli.Create(r.Context(), id, req.Protocol, req.Host, req.Port, req.Username, req.Password)
	if err != nil {
		h.logger.Error("failed to create cli credential", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), id, "CLICredentialCreated", map[string]any{
		"credential_id": cred.ID, "protocol": cred.Protocol, "host": cred.Host, "port": cred.Port,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusCreated, toCLICredentialResponse(*cred))
}

func (h *handler) listCLICredentials(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	creds, err := h.cli.ListByDevice(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to list cli credentials", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]cliCredentialResponse, len(creds))
	for i, c := range creds {
		items[i] = toCLICredentialResponse(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) deleteCLICredential(w http.ResponseWriter, r *http.Request) {
	credentialID := r.PathValue("credential_id")
	// audit H-3: this route is credential-addressed with no device_id in
	// the path at all — without loading the credential to check its own
	// device against the caller's scope first, any operator could delete
	// any tenant's stored SSH/Telnet credentials by UUID alone.
	cred, err := h.cli.ByID(r.Context(), credentialID)
	if errors.Is(err, cliaccess.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to load cli credential", "err", err, "credential_id", credentialID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, ok := h.getScopedDevice(w, r, cred.DeviceID); !ok {
		return
	}
	if err := h.cli.Delete(r.Context(), credentialID); errors.Is(err, cliaccess.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to delete cli credential", "err", err, "credential_id", credentialID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// connectCLI upgrades to a WebSocket and bridges it to the device's real
// SSH/Telnet port for the lifetime of the connection (internal/cliaccess's
// BridgeSSH/BridgeTelnet). Auth still applies — see bearerToken's ?token=
// fallback, since a browser WebSocket can't set the Authorization header.
func (h *handler) connectCLI(w http.ResponseWriter, r *http.Request) {
	credentialID := r.URL.Query().Get("credential_id")
	cred, err := h.cli.ByID(r.Context(), credentialID)
	if errors.Is(err, cliaccess.ErrNotFound) {
		http.Error(w, "credential not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to load cli credential", "err", err, "credential_id", credentialID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The credential belongs to a device; the caller must be in that
	// device's tenancy scope (audit P0.2).
	if _, ok := h.getScopedDevice(w, r, cred.DeviceID); !ok {
		return
	}
	// The bridge dials an operator-configured host from inside the ACS —
	// keep it pointed at device networks, not the ACS's own
	// infrastructure (audit P0.4, same policy as the web-GUI proxy).
	if err := h.netPolicy.CheckHost(r.Context(), cred.Host); err != nil {
		h.logger.Warn("cli bridge target rejected by network policy", "err", err, "credential_id", credentialID, "host", cred.Host)
		http.Error(w, "target host is not allowed: "+err.Error(), http.StatusForbidden)
		return
	}

	websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		ws.PayloadType = websocket.BinaryFrame

		var bridgeErr error
		switch cred.Protocol {
		case cliaccess.ProtocolSSH:
			bridgeErr = cliaccess.BridgeSSH(r.Context(), cred, ws, h.cli.TOFUHostKeyCallback(r.Context(), cred.DeviceID), h.netPolicy)
		case cliaccess.ProtocolTelnet:
			bridgeErr = cliaccess.BridgeTelnet(r.Context(), cred, ws, h.netPolicy)
		}
		if bridgeErr != nil {
			h.logger.Warn("cli bridge ended", "err", bridgeErr, "credential_id", credentialID, "protocol", cred.Protocol)
			_, _ = ws.Write([]byte("\r\n[connection ended: " + bridgeErr.Error() + "]\r\n"))
		}
	}).ServeHTTP(w, r)
}
