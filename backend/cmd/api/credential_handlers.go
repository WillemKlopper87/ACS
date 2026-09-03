// ACS-to-CPE Connection Request credential rotation (build plan §4
// Phase 6 / design doc v3 §11.6). Passwords never appear in a REST
// response from this file — an operator never needs to know the value,
// only the ACS's own Connection Request client does, and §11.7/§11.8
// both call for secrets to stay masked.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"acs/internal/credentials"
	"acs/internal/devices/adapters"
	"acs/internal/jobs"
)

type credentialResponse struct {
	ID          string  `json:"id"`
	Version     int     `json:"version"`
	Username    string  `json:"username"`
	Status      string  `json:"status"`
	CommandKey  string  `json:"command_key,omitempty"`
	CreatedAt   string  `json:"created_at"`
	ActivatedAt *string `json:"activated_at,omitempty"`
	RevokedAt   *string `json:"revoked_at,omitempty"`
}

func toCredentialResponse(c *credentials.Credential) credentialResponse {
	resp := credentialResponse{
		ID: c.ID, Version: c.Version, Username: c.Username, Status: c.Status,
		CommandKey: c.CommandKey, CreatedAt: c.CreatedAt.Format(time.RFC3339),
	}
	if c.ActivatedAt != nil {
		s := c.ActivatedAt.Format(time.RFC3339)
		resp.ActivatedAt = &s
	}
	if c.RevokedAt != nil {
		s := c.RevokedAt.Format(time.RFC3339)
		resp.RevokedAt = &s
	}
	return resp
}

// rotateDeviceCredential implements v3 §11.6 steps 1-3: generate a new
// credential, queue the SetParameterValues that pushes it to the CPE,
// and record it PENDING pending confirmation. It does not activate the
// credential — that's a separate call (activateDeviceCredential), so an
// operator (or future automation) can confirm the job actually succeeded
// before switching the ACS's own Connection Request client over.
func (h *handler) rotateDeviceCredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	// Optional body selects the direction: CONNECTION_REQUEST (default,
	// ACS->CPE) or CWMP_DIGEST (CPE->ACS, per-device Digest identity —
	// activates itself on the CPE's first authenticated Inform).
	credType := credentials.TypeConnectionRequest
	if r.ContentLength != 0 {
		var req struct {
			CredentialType string `json:"credential_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.CredentialType != "" {
			if !credentials.ValidType(req.CredentialType) {
				http.Error(w, "credential_type must be CONNECTION_REQUEST or CWMP_DIGEST", http.StatusBadRequest)
				return
			}
			credType = req.CredentialType
		}
	}

	username, password, err := credentials.GenerateUsernamePassword()
	if err != nil {
		h.logger.Error("failed to generate credential", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	userParam, passParam := adapters.ManagementServerConnectionRequestUser, adapters.ManagementServerConnectionRequestPass
	if credType == credentials.TypeCWMPDigest {
		userParam, passParam = adapters.ManagementServerUsername, adapters.ManagementServerPassword
	}
	usernamePath, _ := adapters.ResolvePath(device.DataModelRoot, userParam)
	passwordPath, _ := adapters.ResolvePath(device.DataModelRoot, passParam)

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeSetParameter, jobs.SetParameterPayload{
		Parameters: []jobs.ParameterWrite{
			{Name: usernamePath, Value: username, Type: "xsd:string"},
			{Name: passwordPath, Value: password, Type: "xsd:string"},
		},
	}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue credential rotation", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	cred, err := h.credentials.Create(r.Context(), id, credType, username, password, job.CommandKey)
	if err != nil {
		h.logger.Error("failed to record new credential", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), id, "CredentialRotation", map[string]any{
		"credential_id": cred.ID, "version": cred.Version, "command_key": job.CommandKey, "type": credType,
		"phase": "started", "username": "***", "password": "***",
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	writeJSON(w, http.StatusAccepted, toCredentialResponse(cred))
}

func (h *handler) listDeviceCredentials(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	list, err := h.credentials.ListByDevice(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to list device credentials", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	items := make([]credentialResponse, 0, len(list))
	for _, c := range list {
		items = append(items, toCredentialResponse(&c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// activateDeviceCredential implements v3 §11.6 steps 4-5: the operator
// (having checked GET /jobs/{command_key} shows the rotation's
// SetParameterValues job SUCCESS) switches the ACS's Connection Request
// client to the new credential. Whatever was ACTIVE before moves to
// GRACE, not straight to REVOKED — v3 is explicit that a rollback path
// should exist, not an instant cutover.
func (h *handler) activateDeviceCredential(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, deviceID); !ok {
		return
	}
	credID := r.PathValue("credential_id")

	// audit H-3: the scope check above only covers the path device —
	// without this, a scoped operator could pass their own device_id
	// (to clear that check) alongside an arbitrary foreign credential_id
	// and activate another tenant's PENDING rotation.
	if existing, err := h.credentials.ByID(r.Context(), credID); errors.Is(err, credentials.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to load credential", "err", err, "credential_id", credID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if existing.DeviceID != deviceID {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	cred, err := h.credentials.Activate(r.Context(), credID)
	if errors.Is(err, credentials.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to activate credential", "err", err, "credential_id", credID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), deviceID, "CredentialRotation", map[string]any{
		"credential_id": cred.ID, "version": cred.Version, "phase": "activated",
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	writeJSON(w, http.StatusOK, toCredentialResponse(cred))
}

// revokeDeviceCredential implements v3 §11.6 step 6: end a GRACE
// credential's validity once its grace window has passed (or abandon a
// PENDING rotation that was never activated).
func (h *handler) revokeDeviceCredential(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, deviceID); !ok {
		return
	}
	credID := r.PathValue("credential_id")

	// audit H-3: same cross-device check as activateDeviceCredential —
	// without it a scoped operator could revoke another tenant's ACTIVE
	// credential by pairing their own device_id with a foreign
	// credential_id, breaking that device's Connection Request auth.
	if existing, err := h.credentials.ByID(r.Context(), credID); errors.Is(err, credentials.ErrNotFound) {
		http.Error(w, "not found (or not in a revocable state)", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to load credential", "err", err, "credential_id", credID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if existing.DeviceID != deviceID {
		http.Error(w, "not found (or not in a revocable state)", http.StatusNotFound)
		return
	}

	cred, err := h.credentials.Revoke(r.Context(), credID)
	if errors.Is(err, credentials.ErrNotFound) {
		http.Error(w, "not found (or not in a revocable state)", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to revoke credential", "err", err, "credential_id", credID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), deviceID, "CredentialRotation", map[string]any{
		"credential_id": cred.ID, "version": cred.Version, "phase": "revoked",
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	writeJSON(w, http.StatusOK, toCredentialResponse(cred))
}
