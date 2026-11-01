package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"acs/internal/auth"
	"acs/internal/bss"
	"acs/internal/secretstore"
)

// Sealed secret handles (I4): POST /bss/v1/secrets stores a secret and
// returns a single-use handle; an order names it (wifi_password_handle)
// instead of the value; the CWMP gateway opens it when it sends the write.
// The value is never in bss_orders, a job, an audit record, an event or a
// log line. See docs/integration/02-integration-model.md §9.

const (
	defaultSecretTTL = 15 * time.Minute
	minSecretTTL     = time.Minute
)

var (
	errSecretHandleInvalid = errors.New("secret handle invalid")
	errPlainSecret         = errors.New("plain secret refused")
)

// secretError is a claimSecrets refusal: one line (it becomes a TMF641
// item note) that matches its kind with errors.Is.
type secretError struct {
	kind error
	msg  string
}

func (e secretError) Error() string        { return e.msg }
func (e secretError) Is(target error) bool { return target == e.kind }

// secretTTLFromEnv reads ACS_BSS_SECRET_TTL, the longest (and default)
// life of a handle. An unparsable or out-of-range value keeps the default.
func secretTTLFromEnv(logger *slog.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv("ACS_BSS_SECRET_TTL"))
	if raw == "" {
		return defaultSecretTTL
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl < minSecretTTL || ttl > 24*time.Hour {
		logger.Warn("ignoring invalid ACS_BSS_SECRET_TTL (1m to 24h)", "value", raw, "default", defaultSecretTTL)
		return defaultSecretTTL
	}
	return ttl
}

// secretOwner is the owner a request's handles belong to: the client's
// customer, or "" for a global client (and the shared token or lab mode,
// which act as one). An account-listed client may not use handles: its
// handles would be shared with every other such client.
func (h *handler) secretOwner(r *http.Request) (string, bool) {
	if !h.requireCustomerBound {
		if h.token == "" && len(h.oauthSigningSecret) == 0 {
			return "", true
		}
		if h.token != "" {
			got, want := r.Header.Get("Authorization"), "Bearer "+h.token
			if len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
				return "", true
			}
		}
	}
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(h.oauthSigningSecret) == 0 {
		return "", false
	}
	claims, err := auth.VerifyJWT(h.oauthSigningSecret, bearer)
	if err != nil || claims.Role != bssClientRole {
		return "", false
	}
	return secretOwnerOf(claims)
}

func secretOwnerOf(claims *auth.Claims) (string, bool) {
	switch {
	case claims.CustomerID != "":
		return claims.CustomerID, true
	case claims.GlobalAccess:
		return "", true
	}
	return "", false
}

type createSecretRequest struct {
	Value      string `json:"value"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// createSecret is POST /bss/v1/secrets.
func (h *handler) createSecret(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.tmfPrincipal(w, r, bss.ScopeTMFWrite)
	if !ok {
		return
	}
	if r.TLS == nil && !h.secretsBehindTLSProxy {
		writeError(w, http.StatusForbidden, "ErrTLSRequired", "secrets are accepted only over TLS")
		return
	}
	owner, ok := secretOwnerOf(claims)
	if !ok {
		writeError(w, http.StatusForbidden, "ErrClientNotCustomerBound", "secret handles need a customer-bound or global client")
		return
	}
	if !h.secrets.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "ErrSecretStoreDisabled", "this ACS has no secret store key ("+secretstore.KeyEnv+")")
		return
	}
	var req createSecretRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "invalid JSON body")
		return
	}
	if req.Value == "" || len(req.Value) > secretstore.MaxValueLength || !utf8.ValidString(req.Value) || strings.ContainsRune(req.Value, 0) {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "value must be 1 to 256 bytes of UTF-8 text")
		return
	}
	ttl := h.secretTTL
	if req.TTLSeconds != 0 {
		asked := time.Duration(req.TTLSeconds) * time.Second
		if asked < minSecretTTL || asked > h.secretTTL {
			writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "ttl_seconds must be between 60 and "+strconv.Itoa(int(h.secretTTL/time.Second)))
			return
		}
		ttl = asked
	}
	handle, err := h.secrets.Put(r.Context(), owner, req.Value, ttl)
	if err != nil {
		h.logger.Error("failed to store secret", "err", err)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if err := h.auditor.Record(r.Context(), "bss:"+claims.Subject, "", "BSSSecretStored", map[string]any{
		"handle": handle.ID, "customer_id": owner, "expires_at": handle.ExpiresAt,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, handle)
}

// deleteSecret is DELETE /bss/v1/secrets/{handle}: the BSS discards a
// handle it will not use. Another owner's handle is 404.
func (h *handler) deleteSecret(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.tmfPrincipal(w, r, bss.ScopeTMFWrite)
	if !ok {
		return
	}
	owner, ok := secretOwnerOf(claims)
	if !ok {
		writeError(w, http.StatusForbidden, "ErrClientNotCustomerBound", "secret handles need a customer-bound or global client")
		return
	}
	deleted, err := h.secrets.Discard(r.Context(), r.PathValue("handle"), owner)
	if err != nil {
		h.logger.Error("failed to discard secret", "err", err)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "ErrNotFound", "resource not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// claimSecrets runs after an order's action is translated and before it
// is dispatched: it refuses a plain passphrase when handles are required
// (ACS_BSS_REQUIRE_SECRET_HANDLES) and claims each handle in writes for
// deviceID, so no other order can use it. With claim false (preflight)
// it only checks them.
func (h *handler) claimSecrets(ctx context.Context, r *http.Request, input map[string]string, writes []bss.ParameterWrite, deviceID string, claim bool) error {
	if h.requireSecretHandles && input["wifi_password"] != "" {
		return secretError{errPlainSecret, "this ACS accepts Wi-Fi passphrases only as wifi_password_handle (POST /bss/v1/secrets)"}
	}
	handles := bss.SecretHandles(writes)
	if len(handles) == 0 {
		return nil
	}
	owner, ok := h.secretOwner(r)
	if !ok {
		return secretError{errSecretHandleInvalid, "secret handles need a customer-bound or global client"}
	}
	for _, handle := range handles {
		var err error
		if claim {
			err = h.secrets.Claim(ctx, handle, owner, deviceID)
		} else {
			err = h.secrets.Check(ctx, handle, owner)
		}
		switch {
		case errors.Is(err, secretstore.ErrInvalidHandle), errors.Is(err, secretstore.ErrDisabled):
			return secretError{errSecretHandleInvalid, "wifi_password_handle " + handle + " is unknown, expired or already used; store the secret again"}
		case err != nil:
			return err
		}
	}
	return nil
}

// writeSecretError answers a claimSecrets error; it reports false for any
// other error, which the caller handles.
func writeSecretError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errPlainSecret):
		// An existing call never answers a new code: a plain passphrase
		// was always a valid order, so its refusal stays ErrInvalidRequest.
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", err.Error())
	case errors.Is(err, errSecretHandleInvalid):
		writeError(w, http.StatusBadRequest, "ErrSecretHandleInvalid", err.Error())
	default:
		return false
	}
	return true
}

// sweepSecrets deletes expired handles; it runs with the sales sync.
func (h *handler) sweepSecrets(ctx context.Context) {
	if !h.secrets.Enabled() {
		return
	}
	if n, err := h.secrets.Sweep(ctx); err != nil {
		h.logger.Error("failed to sweep expired secret handles", "err", err)
	} else if n > 0 {
		h.logger.Info("expired secret handles deleted", "count", n)
	}
}
