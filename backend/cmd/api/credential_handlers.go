// ACS-to-CPE Connection Request credential rotation (build plan §4
// Phase 6 / design doc v3 §11.6). Passwords never appear in a REST
// response from this file — an operator never needs to know the value,
// only the ACS's own Connection Request client does, and §11.7/§11.8
// both call for secrets to stay masked.
package main

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"acs/internal/credentials"
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
	if _, err := h.devices.Get(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to get device", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	username, password, err := credentials.GenerateUsernamePassword()
	if err != nil {
		h.logger.Error("failed to generate credential", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeSetParameter, jobs.SetParameterPayload{
		Parameters: []jobs.ParameterWrite{
			{Name: "Device.ManagementServer.ConnectionRequestUsername", Value: username, Type: "xsd:string"},
			{Name: "Device.ManagementServer.ConnectionRequestPassword", Value: password, Type: "xsd:string"},
		},
	}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue credential rotation", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	cred, err := h.credentials.Create(r.Context(), id, credentials.TypeConnectionRequest, username, password, job.CommandKey)
	if err != nil {
		h.logger.Error("failed to record new credential", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), id, "CredentialRotation", map[string]any{
		"credential_id": cred.ID, "version": cred.Version, "command_key": job.CommandKey,
		"phase": "started", "username": "***", "password": "***",
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	writeJSON(w, http.StatusAccepted, toCredentialResponse(cred))
}

func (h *handler) listDeviceCredentials(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	credID := r.PathValue("credential_id")

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
	credID := r.PathValue("credential_id")

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
