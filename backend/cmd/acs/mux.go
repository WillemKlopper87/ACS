package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"acs/internal/auth"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/observability"
	"acs/internal/ratelimit"
)

// cwmpRouteLabel is the Prometheus "route" label for every CWMP request.
// It is deliberately a constant: the gateway is a catch-all, so labelling
// by r.URL.Path would let any internet scanner mint an unbounded number of
// series. Misconfigured device URLs stay visible in handleCWMP's own log
// line, which is the right place for a high-cardinality value.
const cwmpRouteLabel = "CWMP"

// newACSMux builds the CWMP gateway's HTTP surface. It exists as a
// separate constructor so the wiring — specifically that the CWMP handler
// is instrumented — is testable without standing up a database, a session
// store, or a listener.
func newACSMux(cwmpHandler http.HandlerFunc, metrics *observability.Metrics, db *sql.DB) *http.ServeMux {
	mux := http.NewServeMux()
	// Catch-all rather than exact "/cwmp": CPEs in the field get provisioned
	// with ACS URLs like "http://host:7547/", "/cwmp/", or "/acs", and a 404
	// on the path mismatch looks to the operator exactly like "device won't
	// connect". Any POST that reaches this server is treated as CWMP;
	// handleCWMP logs the path so misconfigured device URLs stay visible.
	//
	// Wrapped in InstrumentHTTP so acs_http_requests_total{service="acs"}
	// exists at all: cmd/api and cmd/bssadapter have always instrumented
	// their routes, this one never did, which left the CWMPAuthFailures
	// alert querying a series that could not exist.
	//
	// Security goal #59: an explicitly configured bootstrap credential is
	// intercepted before the normal CWMP session handler. The constrained
	// path can acknowledge a first-contact Inform, but it cannot open an ACS
	// session, use cookie/IP fallback, evaluate policies or dispatch queued
	// work. The production transport guard remains the outermost boundary.
	cwmpHandler = bootstrapCWMPGuard(cwmpHandler, metrics, db)
	mux.HandleFunc("/", metrics.InstrumentHTTP(cwmpRouteLabel, productionCWMPGuard(cwmpHandler)))
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("GET /healthz", observability.LivenessHandler())
	mux.Handle("GET /readyz", observability.ReadinessHandler(db))
	return mux
}

// bootstrapDeviceLookup is deliberately narrow so the bootstrap admission
// rule is unit-testable without opening Postgres. Production wires it to the
// exact devices.oui_serial lookup used by normal Inform identity binding.
type bootstrapDeviceLookup func(context.Context, string) (*devices.Device, error)

// bootstrapCWMPGuard creates the explicit first-contact bootstrap boundary.
// No configured pair means no bootstrap capability at all; startup validation
// rejects partial pairs before this is constructed.
func bootstrapCWMPGuard(next http.HandlerFunc, metrics *observability.Metrics, db *sql.DB) http.HandlerFunc {
	bootstrapAuth := configuredBootstrapDigestAuthenticator(db)
	if !bootstrapAuth.Enabled() {
		return next
	}
	if db == nil {
		// A configured bootstrap path without its authoritative device store
		// must fail closed rather than silently falling through to normal CWMP.
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "CWMP bootstrap device store unavailable", http.StatusServiceUnavailable)
		}
	}

	deviceRepo := devices.NewRepository(db)
	return bootstrapCWMPGuardWithDeps(
		next,
		bootstrapAuth,
		metrics,
		ratelimit.New(envOrFloat("ACS_RATE_LIMIT_IP_PER_SECOND", defaultIPRateLimitPerSecond), envOrInt("ACS_RATE_LIMIT_IP_BURST", defaultIPRateLimitBurst), rateLimitIdleTTL),
		ratelimit.New(envOrFloat("ACS_RATE_LIMIT_DEVICE_PER_SECOND", defaultDeviceRateLimitPerSecond), envOrInt("ACS_RATE_LIMIT_DEVICE_BURST", defaultDeviceRateLimitBurst), rateLimitIdleTTL),
		deviceRepo.GetByOUIserial,
	)
}

// configuredBootstrapDigestAuthenticator deliberately reuses the normal CWMP
// nonce-signing material and algorithm list. The unauthenticated first Inform
// is challenged by the existing device-bound authenticator; sharing the nonce
// key lets the subsequent request authenticate into this narrower scope while
// keeping the credential itself distinct. Replay state is shared in Postgres
// for the same cross-replica protection as normal device Digest credentials.
func configuredBootstrapDigestAuthenticator(db *sql.DB) auth.DigestAuthenticator {
	bootstrapAuth := auth.DigestAuthenticator{
		Username: strings.TrimSpace(os.Getenv("ACS_CWMP_BOOTSTRAP_USERNAME")),
		Password: os.Getenv("ACS_CWMP_BOOTSTRAP_PASSWORD"),
	}
	if db != nil {
		bootstrapAuth.ReplayStore = auth.NewPostgresReplayStore(db)
	}

	if sharedPassword := os.Getenv("ACS_DIGEST_PASSWORD"); sharedPassword != "" {
		bootstrapAuth.NonceSecret = []byte(sharedPassword)
	} else {
		bootstrapAuth.NonceSecret = []byte(os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"))
	}
	if list := strings.TrimSpace(os.Getenv("ACS_DIGEST_ALGORITHMS")); list != "" {
		for _, algorithm := range strings.Split(list, ",") {
			if algorithm = strings.TrimSpace(algorithm); algorithm != "" {
				bootstrapAuth.Algorithms = append(bootstrapAuth.Algorithms, algorithm)
			}
		}
	}
	return bootstrapAuth
}

// bootstrapCWMPGuardWithDeps contains the constrained bootstrap request path.
// It is intentionally incapable of reaching the normal session handler after
// a bootstrap credential authenticates. That structural separation is the
// control that prevents bootstrap from consuming an established device's
// queue, firmware/diagnostic work, session cookie or policy/provisioning flow.
func bootstrapCWMPGuardWithDeps(
	next http.HandlerFunc,
	bootstrapAuth auth.DigestAuthenticator,
	metrics *observability.Metrics,
	ipLimiter, deviceLimiter *ratelimit.Limiter,
	lookup bootstrapDeviceLookup,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next(w, r)
			return
		}

		// A verified client certificate is already a stronger, device-bound
		// principal. Let the existing mTLS path enforce its CommonName binding
		// rather than downgrading that request into bootstrap scope merely
		// because it also carried an Authorization header.
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			next(w, r)
			return
		}

		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		if bootstrapAuth.Username == "" ||
			!hasAuthScheme(authorization, "Digest") ||
			digestAuthorizationUsername(authorization) != bootstrapAuth.Username {
			next(w, r)
			return
		}

		if ipLimiter != nil && !ipLimiter.Allow(remoteIP(r)) {
			if metrics != nil {
				metrics.RateLimitRejectedTotal.Inc()
			}
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		ok, stale, _ := bootstrapAuth.Verify(r)
		if !ok {
			// Drain the encoded body before the challenge so embedded clients
			// can reuse the keep-alive connection for their authenticated retry.
			_, _ = io.Copy(io.Discard, r.Body)
			if stale {
				bootstrapAuth.ChallengeStale(w)
			} else {
				bootstrapAuth.Challenge(w)
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
		if env.Body.IsEmpty() {
			// Element 1 intentionally has no graduation RPC yet. Close the
			// bootstrap exchange cleanly rather than allowing an empty poll to
			// enter dispatch. Element 2 will use this isolated slot to install
			// the unique per-device credential.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if env.Body.Inform == nil {
			http.Error(w, "bootstrap credential is restricted to first-contact Inform", http.StatusForbidden)
			return
		}

		inform := env.Body.Inform
		inform.DeviceId = inform.DeviceId.Normalized()
		if inform.DeviceId.OUI == "" || inform.DeviceId.SerialNumber == "" {
			http.Error(w, "Inform DeviceId requires OUI and SerialNumber", http.StatusBadRequest)
			return
		}
		naturalKey := inform.DeviceId.NaturalKey()
		if deviceLimiter != nil && !deviceLimiter.Allow(naturalKey) {
			if metrics != nil {
				metrics.RateLimitRejectedTotal.Inc()
			}
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		existing, lookupErr := lookup(r.Context(), naturalKey)
		switch {
		case lookupErr == nil && bootstrapDeviceIsEstablished(existing):
			// This is the critical security boundary: a fleet bootstrap secret
			// cannot claim a device that has already completed a real Inform,
			// regardless of tenant/customer ownership.
			http.Error(w, "bootstrap credential cannot authenticate an established device", http.StatusForbidden)
			return
		case lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows):
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if metrics != nil {
			metrics.InformsTotal.Inc()
		}
		respID := env.Header.ID
		if respID == "" {
			respID = cwmp.NewID()
		}
		ns := cwmp.DetectCWMPNamespace(raw)
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cwmp.RenderInformResponseNS(respID, ns))
	}
}

// bootstrapDeviceIsEstablished distinguishes an operator pre-registration
// (which may exist before first contact) from a device that has already been
// managed. Pre-registration alone is safe to acknowledge because this Element
// 1 path performs no mutation or dispatch; any prior Inform/auth state blocks
// the shared bootstrap credential outright.
func bootstrapDeviceIsEstablished(device *devices.Device) bool {
	if device == nil {
		return false
	}
	if device.LastInformAt != nil {
		return true
	}
	mode := strings.ToUpper(strings.TrimSpace(device.CWMPAuthMode))
	return mode != "" && mode != devices.AuthModeNone
}

// productionCWMPGuard enforces the production side of the lab/production
// split without changing the compatibility behavior used by controlled
// hardware qualification. ACS_DEPLOYMENT_PROFILE defaults to lab elsewhere;
// only the exact value "production" activates these restrictions.
func productionCWMPGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("ACS_DEPLOYMENT_PROFILE")), "production") {
			if r.TLS == nil {
				http.Error(w, "CWMP TLS required in production", http.StatusUpgradeRequired)
				return
			}

			authorization := strings.TrimSpace(r.Header.Get("Authorization"))
			if hasAuthScheme(authorization, "Basic") {
				http.Error(w, "HTTP Basic is not permitted for production CWMP", http.StatusForbidden)
				return
			}

			// A fleet-wide username has no cryptographic binding to one ACS
			// device. Reject it outright in production so a leaked compatibility
			// secret cannot claim any tenant device. Controlled lab use remains
			// available; production onboarding uses the distinct constrained
			// ACS_CWMP_BOOTSTRAP_* scope above, never this legacy identity.
			sharedUsername := strings.TrimSpace(os.Getenv("ACS_DIGEST_USERNAME"))
			if sharedUsername != "" && hasAuthScheme(authorization, "Digest") && digestAuthorizationUsername(authorization) == sharedUsername {
				http.Error(w, "shared CWMP credential is not permitted in production", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func hasAuthScheme(header, scheme string) bool {
	if header == "" {
		return false
	}
	parts := strings.SplitN(header, " ", 2)
	return len(parts) == 2 && strings.EqualFold(parts[0], scheme)
}

// digestAuthorizationUsername extracts only the Digest username directive.
// It is deliberately small and conservative: the real Digest parser and
// cryptographic verification still run in internal/auth; this helper exists
// only to reject use of the configured shared username at the production
// boundary before that compatibility credential can enter a CWMP session.
func digestAuthorizationUsername(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Digest") {
		return ""
	}
	for _, directive := range strings.Split(parts[1], ",") {
		kv := strings.SplitN(strings.TrimSpace(directive), "=", 2)
		if len(kv) != 2 || !strings.EqualFold(strings.TrimSpace(kv[0]), "username") {
			continue
		}
		return strings.Trim(strings.TrimSpace(kv[1]), `"`)
	}
	return ""
}
