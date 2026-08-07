// RBAC admin surface (admin-platform backlog): the superadmin-configurable
// permission matrix (migration 0032), superadmin password reset, and
// self-service password reset via emailed token.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"acs/internal/operators"
)

// getRolePermissions returns the full role x permission matrix —
// superadmin-only (it's the mechanism superadmin uses to decide what
// manager/noc/readonly can do, so viewing it needs the same gate as
// editing it).
func (h *handler) getRolePermissions(w http.ResponseWriter, r *http.Request) {
	matrix, err := h.permissions.All(r.Context())
	if err != nil {
		h.logger.Error("failed to load role permissions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"roles":       []string{operators.RoleManager, operators.RoleNOC, operators.RoleReadOnly},
		"permissions": operators.AllPermissions,
		"matrix":      matrix,
	})
}

type setRolePermissionRequest struct {
	Role       string `json:"role"`
	Permission string `json:"permission"`
	Granted    bool   `json:"granted"`
}

func (h *handler) setRolePermission(w http.ResponseWriter, r *http.Request) {
	var req setRolePermissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Role != operators.RoleManager && req.Role != operators.RoleNOC && req.Role != operators.RoleReadOnly {
		http.Error(w, "role must be manager, noc, or readonly (superadmin always has every permission and isn't configurable)", http.StatusBadRequest)
		return
	}
	valid := false
	for _, p := range operators.AllPermissions {
		if p == req.Permission {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, "unknown permission key", http.StatusBadRequest)
		return
	}

	if err := h.permissions.Set(r.Context(), req.Role, req.Permission, req.Granted); err != nil {
		h.logger.Error("failed to set role permission", "err", err, "role", req.Role, "permission", req.Permission)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "RolePermissionChanged", map[string]any{
		"role": req.Role, "permission": req.Permission, "granted": req.Granted,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("role permission changed", "role", req.Role, "permission", req.Permission, "granted", req.Granted, "changed_by", actor)
	writeJSON(w, http.StatusOK, req)
}

type resetOperatorPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// resetOperatorPassword is the superadmin "change this user's password
// from the account page" action — sets it directly, no token/email
// involved, distinct from the self-service flow below.
func (h *handler) resetOperatorPassword(w http.ResponseWriter, r *http.Request) {
	operatorID := r.PathValue("id")
	var req resetOperatorPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.NewPassword) < 8 {
		http.Error(w, "new_password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		h.logger.Error("failed to hash password", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.operators.UpdatePassword(r.Context(), operatorID, string(hash)); err != nil {
		h.logger.Error("failed to reset operator password", "err", err, "operator_id", operatorID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "OperatorPasswordReset", map[string]any{"operator_id": operatorID}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("operator password reset by superadmin", "operator_id", operatorID, "reset_by", actor)
	w.WriteHeader(http.StatusNoContent)
}

type requestPasswordResetRequest struct {
	Username string `json:"username"`
}

// requestPasswordReset is the public (unauthenticated — see isPublicRoute)
// self-service entry point. Always responds 202 regardless of whether the
// username/email actually resolved to anything, so this endpoint can't be
// used to enumerate valid usernames.
func (h *handler) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req requestPasswordResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	op, err := h.operators.ByUsername(r.Context(), req.Username)
	if err != nil || op.Email == "" {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			h.logger.Error("failed to look up operator for password reset", "err", err)
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	token, expiresAt, err := h.operators.CreateResetToken(r.Context(), op.ID)
	if err != nil {
		h.logger.Error("failed to create password reset token", "err", err, "operator_id", op.ID)
		w.WriteHeader(http.StatusAccepted) // still don't leak failure detail to the caller
		return
	}

	resetLink := fmt.Sprintf("%s/reset-password?token=%s", h.frontendBaseURL, token)
	body := fmt.Sprintf("A password reset was requested for your ACS console account (%s).\n\n"+
		"Reset your password: %s\n\nThis link expires at %s. If you didn't request this, you can ignore this email.",
		op.Username, resetLink, expiresAt.Format("2006-01-02 15:04 MST"))

	if err := h.mailer.Send(op.Email, "ACS console password reset", body); err != nil {
		h.logger.Error("failed to send password reset email", "err", err, "operator_id", op.ID)
	}
	if err := h.auditor.Record(r.Context(), op.Username, "", "PasswordResetRequested", map[string]any{"operator_id": op.ID}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusAccepted)
}

type confirmPasswordResetRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// confirmPasswordReset is the public (unauthenticated) endpoint the
// emailed link's page ultimately submits to.
func (h *handler) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req confirmPasswordResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.NewPassword) < 8 {
		http.Error(w, "new_password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	op, err := h.operators.ConsumeResetToken(r.Context(), req.Token)
	if errors.Is(err, operators.ErrTokenInvalid) {
		http.Error(w, "reset link is invalid, expired, or already used", http.StatusBadRequest)
		return
	}
	if err != nil {
		h.logger.Error("failed to consume password reset token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		h.logger.Error("failed to hash password", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.operators.UpdatePassword(r.Context(), op.ID, string(hash)); err != nil {
		h.logger.Error("failed to update password after reset", "err", err, "operator_id", op.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(r.Context(), op.Username, "", "PasswordResetCompleted", map[string]any{"operator_id": op.ID}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("password reset completed", "operator_id", op.ID, "username", op.Username)
	w.WriteHeader(http.StatusNoContent)
}
