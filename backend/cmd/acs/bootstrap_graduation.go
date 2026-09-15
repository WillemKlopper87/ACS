package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/credentials"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/observability"
	"acs/internal/ratelimit"
)

const (
	bootstrapCredentialCookieName = "acs_bootstrap_credential"
	bootstrapCredentialCookieTTL  = 10 * time.Minute
)

type bootstrapEnsureDevice func(context.Context, cwmp.DeviceID, string) (*devices.Device, error)
type bootstrapEnsurePendingCredential func(context.Context, string) (*credentials.Credential, error)
type bootstrapCredentialByID func(context.Context, string) (*credentials.Credential, error)
type bootstrapDeviceByID func(context.Context, string) (*devices.Device, error)

type bootstrapGraduationDeps struct {
	bootstrapAuth auth.DigestAuthenticator
	metrics       *observability.Metrics
	ipLimiter     *ratelimit.Limiter
	deviceLimiter *ratelimit.Limiter
	lookup        bootstrapDeviceLookup
	ensureDevice  bootstrapEnsureDevice
	ensurePending bootstrapEnsurePendingCredential
	credentialByID bootstrapCredentialByID
	deviceByID    bootstrapDeviceByID
}

// bootstrapCWMPGraduationGuard owns the production bootstrap->unique-credential
// transition. It sits outside the Element 1 guard; non-bootstrap traffic passes
// through unchanged, while a bootstrap-authenticated exchange is fully handled
// here and can never enter the normal session/job/policy dispatcher.
func bootstrapCWMPGraduationGuard(next http.HandlerFunc, metrics *observability.Metrics, db *sql.DB) http.HandlerFunc {
	bootstrapAuth := configuredBootstrapDigestAuthenticator(db)
	if !bootstrapAuth.Enabled() {
		return next
	}
	if db == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "CWMP bootstrap device store unavailable", http.StatusServiceUnavailable)
		}
	}

	deviceRepo := devices.NewRepository(db)
	credentialRepo, err := credentials.NewRepository(db, os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"))
	if err != nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "CWMP bootstrap credential store unavailable", http.StatusServiceUnavailable)
		}
	}

	return bootstrapCWMPGraduationGuardWithDeps(next, bootstrapGraduationDeps{
		bootstrapAuth: bootstrapAuth,
		metrics:       metrics,
		ipLimiter:     ratelimit.New(envOrFloat("ACS_RATE_LIMIT_IP_PER_SECOND", defaultIPRateLimitPerSecond), envOrInt("ACS_RATE_LIMIT_IP_BURST", defaultIPRateLimitBurst), rateLimitIdleTTL),
		deviceLimiter: ratelimit.New(envOrFloat("ACS_RATE_LIMIT_DEVICE_PER_SECOND", defaultDeviceRateLimitPerSecond), envOrInt("ACS_RATE_LIMIT_DEVICE_BURST", defaultDeviceRateLimitBurst), rateLimitIdleTTL),
		lookup:        deviceRepo.GetByOUIserial,
		ensureDevice: deviceRepo.EnsureBootstrapRegistration,
		ensurePending: credentialRepo.EnsurePendingCWMPDigest,
		credentialByID: credentialRepo.ByID,
		deviceByID:    deviceRepo.Get,
	})
}

func bootstrapCWMPGraduationGuardWithDeps(next http.HandlerFunc, deps bootstrapGraduationDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next(w, r)
			return
		}
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			next(w, r)
			return
		}

		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		if deps.bootstrapAuth.Username == "" ||
			!hasAuthScheme(authorization, "Digest") ||
			digestAuthorizationUsername(authorization) != deps.bootstrapAuth.Username {
			next(w, r)
			return
		}

		if deps.ipLimiter != nil && !deps.ipLimiter.Allow(remoteIP(r)) {
			if deps.metrics != nil {
				deps.metrics.RateLimitRejectedTotal.Inc()
			}
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		ok, stale, _ := deps.bootstrapAuth.Verify(r)
		if !ok {
			_, _ = io.Copy(io.Discard, r.Body)
			if stale {
				deps.bootstrapAuth.ChallengeStale(w)
			} else {
				deps.bootstrapAuth.Challenge(w)
			}
			return
		}

		raw, err := readCWMPBody(w, r)
		if err != nil {
			if _, unsupported := err.(*unsupportedEncodingError); unsupported {
				w.Header().Set("Accept-Encoding", "identity, gzip, deflate")
				http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
				return
			}
			http.Error(w, "body too large, unreadable, or invalidly compressed", http.StatusBadRequest)
			return
		}
		env, err := cwmp.ParseEnvelope(raw)
		if err != nil {
			http.Error(w, "malformed XML", http.StatusBadRequest)
			return
		}

		if env.Body.Inform != nil {
			handleBootstrapGraduationInform(w, r, raw, env, deps)
			return
		}

		cred, device, ns, stateOK := loadBootstrapGraduationState(r, deps)
		if !stateOK {
			clearBootstrapCredentialCookie(w, r)
			http.Error(w, "bootstrap graduation state missing or no longer valid", http.StatusForbidden)
			return
		}
		if bootstrapDeviceIsEstablished(device) {
			clearBootstrapCredentialCookie(w, r)
			http.Error(w, "bootstrap credential cannot manage an established device", http.StatusForbidden)
			return
		}

		switch {
		case env.Body.IsEmpty():
			handleBootstrapCredentialInstall(w, r, cred, device, ns)
		case env.Body.SetParameterValuesResponse != nil:
			// The CPE accepted the credential write. Do not activate here: only
			// a later reconnect that proves the new Digest secret AND claims the
			// credential-bound natural identity graduates PENDING -> ACTIVE.
			clearBootstrapCredentialCookie(w, r)
			w.WriteHeader(http.StatusNoContent)
		case env.Body.Fault != nil:
			// A vendor that cannot rewrite ManagementServer credentials remains
			// safely PENDING for explicit operator/manual recovery. Never fall
			// back to shared normal-management credentials.
			clearBootstrapCredentialCookie(w, r)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "bootstrap credential is restricted to credential graduation", http.StatusForbidden)
		}
	}
}

func handleBootstrapGraduationInform(w http.ResponseWriter, r *http.Request, raw []byte, env *cwmp.Envelope, deps bootstrapGraduationDeps) {
	inform := env.Body.Inform
	inform.DeviceId = inform.DeviceId.Normalized()
	if inform.DeviceId.OUI == "" || inform.DeviceId.SerialNumber == "" {
		http.Error(w, "Inform DeviceId requires OUI and SerialNumber", http.StatusBadRequest)
		return
	}

	naturalKey := inform.DeviceId.NaturalKey()
	if deps.deviceLimiter != nil && !deps.deviceLimiter.Allow(naturalKey) {
		if deps.metrics != nil {
			deps.metrics.RateLimitRejectedTotal.Inc()
		}
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	existing, lookupErr := deps.lookup(r.Context(), naturalKey)
	switch {
	case lookupErr == nil && bootstrapDeviceIsEstablished(existing):
		http.Error(w, "bootstrap credential cannot authenticate an established device", http.StatusForbidden)
		return
	case lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows):
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	root := inferBootstrapDataModelRoot(inform.ParameterList)
	device, err := deps.ensureDevice(r.Context(), inform.DeviceId, root)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Close a race with an operator/real-device Inform between the admission
	// lookup above and the bootstrap-safe upsert.
	if bootstrapDeviceIsEstablished(device) {
		http.Error(w, "bootstrap credential cannot authenticate an established device", http.StatusForbidden)
		return
	}

	cred, err := deps.ensurePending(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ns := cwmp.DetectCWMPNamespace(raw)
	setBootstrapCredentialCookie(w, r, cred.ID, ns)
	if deps.metrics != nil {
		deps.metrics.InformsTotal.Inc()
	}
	respID := env.Header.ID
	if respID == "" {
		respID = cwmp.NewID()
	}
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cwmp.RenderInformResponseNS(respID, ns))
}

func handleBootstrapCredentialInstall(w http.ResponseWriter, r *http.Request, cred *credentials.Credential, device *devices.Device, ns string) {
	if device.DataModelRoot != devices.DataModelRootDevice2 && device.DataModelRoot != devices.DataModelRootIGD1 {
		// Never guess TR-181 for an unknown first-contact model when the write
		// changes the credential needed to reach the ACS again.
		clearBootstrapCredentialCookie(w, r)
		http.Error(w, "device data-model root is unknown; automatic credential installation is unavailable", http.StatusConflict)
		return
	}
	usernamePath, okUser := adapters.ResolvePath(device.DataModelRoot, adapters.ManagementServerUsername)
	passwordPath, okPass := adapters.ResolvePath(device.DataModelRoot, adapters.ManagementServerPassword)
	if !okUser || !okPass {
		http.Error(w, "device credential parameter mapping unavailable", http.StatusConflict)
		return
	}

	rpc := cwmp.RenderSetParameterValues(cwmp.NewID(), []cwmp.ParameterValueStruct{
		{Name: usernamePath, Value: cred.Username},
		{Name: passwordPath, Value: cred.Password},
	}, cred.CommandKey)
	rpc = cwmp.RewriteCWMPNamespace(rpc, ns)
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rpc)
}

func loadBootstrapGraduationState(r *http.Request, deps bootstrapGraduationDeps) (*credentials.Credential, *devices.Device, string, bool) {
	cookie, err := r.Cookie(bootstrapCredentialCookieName)
	if err != nil {
		return nil, nil, "", false
	}
	credentialID, ns, ok := decodeBootstrapCredentialCookie(cookie.Value)
	if !ok {
		return nil, nil, "", false
	}
	cred, err := deps.credentialByID(r.Context(), credentialID)
	if err != nil || cred.CredentialType != credentials.TypeCWMPDigest || cred.Status != credentials.StatusPending {
		return nil, nil, "", false
	}
	device, err := deps.deviceByID(r.Context(), cred.DeviceID)
	if err != nil {
		return nil, nil, "", false
	}
	return cred, device, ns, true
}

func inferBootstrapDataModelRoot(params []cwmp.ParameterValueStruct) string {
	root := devices.DataModelRootUnknown
	for _, p := range params {
		candidate := devices.DataModelRootUnknown
		switch {
		case strings.HasPrefix(p.Name, "InternetGatewayDevice."):
			candidate = devices.DataModelRootIGD1
		case strings.HasPrefix(p.Name, "Device."):
			candidate = devices.DataModelRootDevice2
		default:
			continue
		}
		if root != devices.DataModelRootUnknown && root != candidate {
			return devices.DataModelRootUnknown
		}
		root = candidate
	}
	return root
}

func setBootstrapCredentialCookie(w http.ResponseWriter, r *http.Request, credentialID, ns string) {
	http.SetCookie(w, &http.Cookie{
		Name:     bootstrapCredentialCookieName,
		Value:    encodeBootstrapCredentialCookie(credentialID, ns),
		Path:     "/",
		MaxAge:   int(bootstrapCredentialCookieTTL.Seconds()),
		Expires:  time.Now().Add(bootstrapCredentialCookieTTL),
		Secure:   r.TLS != nil,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearBootstrapCredentialCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     bootstrapCredentialCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		Secure:   r.TLS != nil,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func encodeBootstrapCredentialCookie(credentialID, ns string) string {
	return credentialID + "." + base64.RawURLEncoding.EncodeToString([]byte(ns))
}

func decodeBootstrapCredentialCookie(value string) (credentialID, ns string, ok bool) {
	credentialID, encodedNS, found := strings.Cut(value, ".")
	if !found || credentialID == "" || encodedNS == "" {
		return "", "", false
	}
	rawNS, err := base64.RawURLEncoding.DecodeString(encodedNS)
	if err != nil || len(rawNS) == 0 {
		return "", "", false
	}
	return credentialID, string(rawNS), true
}
