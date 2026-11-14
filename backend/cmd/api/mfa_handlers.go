// Operator MFA: a one-time code from an authenticator app as a second
// login step (docs/CONSOLE-REDESIGN-AND-MFA.md §5).
//
// A password alone never yields a session for an operator with MFA on.
// It yields a short-lived, audience-bound MFA token that only the
// /auth/login/mfa routes accept; the session token is issued once the
// code checks out. An operator whose role must use MFA and who has not
// set it up gets an enrolment token instead, and the session after the
// first code is confirmed.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/mfa"
	"acs/internal/operators"
	"acs/internal/ratelimit"
)

const (
	mfaTokenTTL = 5 * time.Minute
	mfaIssuer   = "ACS"

	mfaStepVerify = "verify"
	mfaStepEnroll = "enroll"
)

// mfaLimiter bounds code guesses per operator: a six-digit code is only
// safe if it cannot be tried many times. Five tries, then one per 12s.
var mfaLimiter = ratelimit.New(1.0/12, 5, 30*time.Minute)

// mfaRequiredRoles parses ACS_MFA_REQUIRED_ROLES: a comma-separated list
// of roles that must use MFA, or "all".
func mfaRequiredRoles(v string) map[string]bool {
	out := map[string]bool{}
	for _, role := range strings.Split(v, ",") {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "all" {
			for _, r := range []string{operators.RoleReadOnly, operators.RoleNOC, operators.RoleManager, operators.RoleSuperAdmin} {
				out[r] = true
			}
		} else if role != "" {
			out[role] = true
		}
	}
	return out
}

type mfaChallengeResponse struct {
	// MFA is "verify" (enter a code) or "enroll" (set MFA up first).
	MFA       string `json:"mfa"`
	MFAToken  string `json:"mfa_token"`
	ExpiresAt string `json:"expires_at"`
}

// writeMFAChallenge answers a correct password with the next step
// instead of a session.
func (h *handler) writeMFAChallenge(w http.ResponseWriter, op *operators.Operator, step string) {
	audience := auth.AudienceMFAVerify
	if step == mfaStepEnroll {
		audience = auth.AudienceMFAEnroll
	}
	now := time.Now().UTC()
	claims := auth.Claims{Subject: op.Username, Role: op.Role, IssuedAt: now, ExpiresAt: now.Add(mfaTokenTTL), Version: op.TokenVersion, Audience: audience}
	token, err := auth.SignJWT(h.jwtSecret, claims)
	if err != nil {
		h.logger.Error("failed to sign MFA token", "err", err, "username", op.Username)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, mfaChallengeResponse{MFA: step, MFAToken: token, ExpiresAt: claims.ExpiresAt.Format(time.RFC3339)})
}

// issueSession signs the session token and records the login.
func (h *handler) issueSession(w http.ResponseWriter, r *http.Request, op *operators.Operator, extra map[string]any) {
	now := time.Now().UTC()
	claims := auth.Claims{Subject: op.Username, Role: op.Role, IssuedAt: now, ExpiresAt: now.Add(jwtTTL), Version: op.TokenVersion}
	token, err := auth.SignJWT(h.jwtSecret, claims)
	if err != nil {
		h.logger.Error("failed to sign JWT", "err", err, "username", op.Username)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Recovery codes go to the operator once, never into the audit trail.
	detail := map[string]any{"role": op.Role}
	for k, v := range extra {
		if k != "recovery_codes" {
			detail[k] = v
		}
	}
	if err := h.auditor.Record(r.Context(), op.Username, "", "OperatorLogin", detail); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("operator logged in", "username", op.Username, "role", op.Role)

	// The console keeps the session in a cookie its scripts cannot read
	// (session_cookie.go). The token stays in the body for clients that
	// send it as a bearer token; the console does not keep it.
	h.setSessionCookie(w, r, token, claims.ExpiresAt)
	body := map[string]any{"token": token, "role": op.Role, "expires_at": claims.ExpiresAt.Format(time.RFC3339), "session_cookie": h.sessionCookies}
	if codes, ok := extra["recovery_codes"]; ok {
		body["recovery_codes"] = codes
	}
	writeJSON(w, http.StatusOK, body)
}

// afterPassword decides what a correct password earns: a session, a code
// prompt, or an enrolment.
func (h *handler) afterPassword(w http.ResponseWriter, r *http.Request, op *operators.Operator) {
	status, err := h.mfa.Status(r.Context(), op.ID)
	if err != nil {
		h.logger.Error("failed to read MFA status", "err", err, "username", op.Username)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch {
	case status.Enabled:
		h.writeMFAChallenge(w, op, mfaStepVerify)
	case h.mfaRequired[op.Role]:
		if !h.mfa.Available() {
			// Fail closed: the role must use MFA and it cannot be set up.
			h.logger.Error("MFA is required for this role but cannot be enrolled", "username", op.Username, "reason", mfa.ErrUnavailable.Error())
			http.Error(w, "MFA is required for this account but is not available on this server: "+mfa.ErrUnavailable.Error(), http.StatusServiceUnavailable)
			return
		}
		h.writeMFAChallenge(w, op, mfaStepEnroll)
	default:
		h.issueSession(w, r, op, nil)
	}
}

type mfaLoginRequest struct {
	MFAToken string `json:"mfa_token"`
	Code     string `json:"code"`
}

// operatorFromMFAToken resolves the operator a login-step token was issued
// to. The token must carry the wanted audience, be unexpired, and predate
// no session revocation or disablement.
func (h *handler) operatorFromMFAToken(w http.ResponseWriter, r *http.Request, audience string) (*operators.Operator, *mfaLoginRequest, bool) {
	if len(h.jwtSecret) == 0 {
		http.Error(w, "operator auth is not configured on this server", http.StatusServiceUnavailable)
		return nil, nil, false
	}
	var req mfaLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return nil, nil, false
	}
	const expired = "this sign-in step has expired — start again with your password"
	claims, err := auth.VerifyJWT(h.jwtSecret, req.MFAToken)
	if err != nil || claims.Audience != audience {
		http.Error(w, expired, http.StatusUnauthorized)
		return nil, nil, false
	}
	op, err := h.operators.ByUsername(r.Context(), claims.Subject)
	if err != nil || op.DisabledAt != nil || op.TokenVersion != claims.Version {
		http.Error(w, expired, http.StatusUnauthorized)
		return nil, nil, false
	}
	return op, &req, true
}

// allowMFAAttempt applies the per-operator guess limit.
func (h *handler) allowMFAAttempt(w http.ResponseWriter, op *operators.Operator) bool {
	if mfaLimiter.Allow(op.ID) {
		return true
	}
	h.metrics.RateLimitRejectedTotal.Inc()
	http.Error(w, "too many code attempts — wait a minute and try again", http.StatusTooManyRequests)
	return false
}

// writeMFAError maps an mfa package refusal to a status. A wrong code is
// 401 at login, where it is the credential; inside a session it is 403,
// because the session itself is still good and a client must not take
// the answer for "signed out".
func (h *handler) writeMFAError(w http.ResponseWriter, err error, what string) {
	h.writeMFAErrorAs(w, err, what, http.StatusUnauthorized)
}

func (h *handler) writeSessionMFAError(w http.ResponseWriter, err error, what string) {
	h.writeMFAErrorAs(w, err, what, http.StatusForbidden)
}

func (h *handler) writeMFAErrorAs(w http.ResponseWriter, err error, what string, wrongCode int) {
	switch {
	case errors.Is(err, mfa.ErrInvalidCode):
		http.Error(w, "that code is not valid — check the app and try the next one", wrongCode)
	case errors.Is(err, mfa.ErrAlreadyEnabled):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, mfa.ErrNotEnrolled):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, mfa.ErrUnavailable):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		h.logger.Error("failed to "+what, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (h *handler) auditMFA(r *http.Request, actor, action string, detail map[string]any) {
	if err := h.auditor.Record(r.Context(), actor, "", action, detail); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
}

// loginMFA is the second login step: an app code or a recovery code.
func (h *handler) loginMFA(w http.ResponseWriter, r *http.Request) {
	op, req, ok := h.operatorFromMFAToken(w, r, auth.AudienceMFAVerify)
	if !ok || !h.allowMFAAttempt(w, op) {
		return
	}
	usedRecovery, err := h.mfa.Check(r.Context(), op.ID, req.Code)
	if err != nil {
		if errors.Is(err, mfa.ErrInvalidCode) {
			h.auditMFA(r, op.Username, "OperatorMFAFailed", map[string]any{"step": "login"})
		}
		h.writeMFAError(w, err, "check MFA code")
		return
	}
	extra := map[string]any{"mfa": "totp"}
	if usedRecovery {
		extra["mfa"] = "recovery_code"
		h.auditMFA(r, op.Username, "OperatorMFARecoveryCodeUsed", nil)
	}
	h.issueSession(w, r, op, extra)
}

type mfaEnrollResponse struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauth_uri"`
}

func (h *handler) beginMFA(w http.ResponseWriter, r *http.Request, op *operators.Operator) {
	secret, err := h.mfa.Begin(r.Context(), op.ID)
	if err != nil {
		h.writeMFAError(w, err, "begin MFA enrolment")
		return
	}
	writeJSON(w, http.StatusOK, mfaEnrollResponse{Secret: secret, OTPAuthURI: mfa.URI(mfaIssuer, op.Username, secret)})
}

// loginMFAEnroll starts enrolment for an operator whose role requires MFA.
func (h *handler) loginMFAEnroll(w http.ResponseWriter, r *http.Request) {
	op, _, ok := h.operatorFromMFAToken(w, r, auth.AudienceMFAEnroll)
	if !ok {
		return
	}
	h.beginMFA(w, r, op)
}

// loginMFAConfirm completes a required enrolment and signs the operator in.
func (h *handler) loginMFAConfirm(w http.ResponseWriter, r *http.Request) {
	op, req, ok := h.operatorFromMFAToken(w, r, auth.AudienceMFAEnroll)
	if !ok || !h.allowMFAAttempt(w, op) {
		return
	}
	codes, err := h.mfa.Confirm(r.Context(), op.ID, req.Code)
	if err != nil {
		h.writeMFAError(w, err, "confirm MFA enrolment")
		return
	}
	h.auditMFA(r, op.Username, "OperatorMFAEnabled", map[string]any{"at": "login"})
	h.issueSession(w, r, op, map[string]any{"mfa": "totp", "recovery_codes": codes})
}

// sessionOperator is the signed-in operator, for the self-service routes.
func (h *handler) sessionOperator(w http.ResponseWriter, r *http.Request) (*operators.Operator, bool) {
	claims, ok := operatorClaims(r.Context())
	if !ok || claims.Subject == serviceSubject {
		http.Error(w, "MFA is managed from an operator session", http.StatusBadRequest)
		return nil, false
	}
	op, err := h.operators.ByUsername(r.Context(), claims.Subject)
	if err != nil {
		http.Error(w, "operator not found", http.StatusNotFound)
		return nil, false
	}
	return op, true
}

type mfaStatusResponse struct {
	Enabled           bool `json:"enabled"`
	Required          bool `json:"required"`
	Available         bool `json:"available"`
	RecoveryCodesLeft int  `json:"recovery_codes_left"`
}

func (h *handler) getMFA(w http.ResponseWriter, r *http.Request) {
	op, ok := h.sessionOperator(w, r)
	if !ok {
		return
	}
	status, err := h.mfa.Status(r.Context(), op.ID)
	if err != nil {
		h.writeSessionMFAError(w, err, "read MFA status")
		return
	}
	writeJSON(w, http.StatusOK, mfaStatusResponse{
		Enabled: status.Enabled, Required: h.mfaRequired[op.Role], Available: h.mfa.Available(), RecoveryCodesLeft: status.RecoveryCodesLeft,
	})
}

func (h *handler) enrollMFA(w http.ResponseWriter, r *http.Request) {
	op, ok := h.sessionOperator(w, r)
	if !ok {
		return
	}
	h.beginMFA(w, r, op)
}

type mfaCodeRequest struct {
	Code string `json:"code"`
}

// codeFromSession reads the code a self-service change must carry and
// applies the guess limit.
func (h *handler) codeFromSession(w http.ResponseWriter, r *http.Request) (*operators.Operator, string, bool) {
	op, ok := h.sessionOperator(w, r)
	if !ok {
		return nil, "", false
	}
	var req mfaCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		http.Error(w, "code is required", http.StatusBadRequest)
		return nil, "", false
	}
	if !h.allowMFAAttempt(w, op) {
		return nil, "", false
	}
	return op, req.Code, true
}

func (h *handler) confirmMFA(w http.ResponseWriter, r *http.Request) {
	op, code, ok := h.codeFromSession(w, r)
	if !ok {
		return
	}
	codes, err := h.mfa.Confirm(r.Context(), op.ID, code)
	if err != nil {
		h.writeSessionMFAError(w, err, "confirm MFA enrolment")
		return
	}
	h.auditMFA(r, op.Username, "OperatorMFAEnabled", map[string]any{"at": "account"})
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// disableMFA turns the caller's own MFA off. It needs a current code, so
// a session left open on a desk is not enough, and is refused where the
// role must use MFA.
func (h *handler) disableMFA(w http.ResponseWriter, r *http.Request) {
	op, code, ok := h.codeFromSession(w, r)
	if !ok {
		return
	}
	if h.mfaRequired[op.Role] {
		http.Error(w, "MFA is required for your role and cannot be turned off", http.StatusConflict)
		return
	}
	if _, err := h.mfa.Check(r.Context(), op.ID, code); err != nil {
		h.writeSessionMFAError(w, err, "check MFA code")
		return
	}
	if err := h.mfa.Disable(r.Context(), op.ID); err != nil {
		h.writeSessionMFAError(w, err, "disable MFA")
		return
	}
	h.auditMFA(r, op.Username, "OperatorMFADisabled", map[string]any{"by": "self"})
	w.WriteHeader(http.StatusNoContent)
}

// regenerateMFARecoveryCodes replaces the caller's recovery codes.
func (h *handler) regenerateMFARecoveryCodes(w http.ResponseWriter, r *http.Request) {
	op, code, ok := h.codeFromSession(w, r)
	if !ok {
		return
	}
	if _, err := h.mfa.Check(r.Context(), op.ID, code); err != nil {
		h.writeSessionMFAError(w, err, "check MFA code")
		return
	}
	codes, err := h.mfa.RegenerateRecoveryCodes(r.Context(), op.ID)
	if err != nil {
		h.writeSessionMFAError(w, err, "regenerate recovery codes")
		return
	}
	h.auditMFA(r, op.Username, "OperatorMFARecoveryCodesRegenerated", nil)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// resetOperatorMFA is the lost-phone path: a superadmin removes another
// operator's MFA and ends their sessions. If their role requires MFA they
// enrol again at their next login.
func (h *handler) resetOperatorMFA(w http.ResponseWriter, r *http.Request) {
	target, err := h.operators.ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "operator not found", http.StatusNotFound)
		return
	}
	if err := h.mfa.Disable(r.Context(), target.ID); err != nil {
		h.writeMFAError(w, err, "reset MFA")
		return
	}
	if err := h.operators.RevokeSessions(r.Context(), target.ID); err != nil {
		h.logger.Error("failed to revoke sessions after MFA reset", "err", err, "operator_id", target.ID)
	}
	h.forgetTokenVersion(target.Username)
	h.auditMFA(r, operatorFromRequest(r), "OperatorMFAReset", map[string]any{"operator": target.Username})
	w.WriteHeader(http.StatusNoContent)
}
