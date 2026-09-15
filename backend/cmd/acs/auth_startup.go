package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"acs/internal/config"
)

const (
	acsDeploymentProfileLab        = "lab"
	acsDeploymentProfileProduction = "production"
)

// loadACSDeploymentProfile makes the lab/production split explicit at process
// startup. An unknown value must never fall through to the compatibility path:
// a misspelled production profile would otherwise disable every production
// request guard while still looking intentional in deployment configuration.
func loadACSDeploymentProfile() (string, error) {
	profile := strings.ToLower(strings.TrimSpace(os.Getenv("ACS_DEPLOYMENT_PROFILE")))
	if profile == "" {
		profile = acsDeploymentProfileLab
	}
	switch profile {
	case acsDeploymentProfileLab, acsDeploymentProfileProduction:
		return profile, nil
	default:
		return "", fmt.Errorf("ACS_DEPLOYMENT_PROFILE must be %q or %q, got %q", acsDeploymentProfileLab, acsDeploymentProfileProduction, profile)
	}
}

// validateCWMPTransportStartup enforces the transport promises made by the
// production profile before the database is opened or the listener starts.
// Lab deliberately retains the compatibility settings used for field
// qualification of older CPEs.
func validateCWMPTransportStartup(profile string) error {
	if profile != acsDeploymentProfileProduction {
		return nil
	}

	certFile := strings.TrimSpace(os.Getenv("ACS_TLS_CERT"))
	keyFile := strings.TrimSpace(os.Getenv("ACS_TLS_KEY"))
	if certFile == "" || keyFile == "" {
		return errors.New("ACS_TLS_CERT and ACS_TLS_KEY are required when ACS_DEPLOYMENT_PROFILE=production")
	}
	if envBool("ACS_AUTH_ALLOW_BASIC") {
		return errors.New("ACS_AUTH_ALLOW_BASIC is forbidden when ACS_DEPLOYMENT_PROFILE=production")
	}

	// Production defaults to TLS 1.2 when ACS_TLS_MIN_VERSION is unset,
	// and explicitly refuses the legacy 1.0/1.1 compatibility floors.
	// Lab continues to support those versions for controlled qualification.
	switch strings.TrimSpace(os.Getenv("ACS_TLS_MIN_VERSION")) {
	case "", "1.2", "1.3":
		return nil
	case "1.0", "1.1":
		return errors.New("ACS_TLS_MIN_VERSION must be 1.2 or 1.3 when ACS_DEPLOYMENT_PROFILE=production")
	default:
		return fmt.Errorf("invalid ACS_TLS_MIN_VERSION %q (want 1.2 or 1.3 in production)", strings.TrimSpace(os.Getenv("ACS_TLS_MIN_VERSION")))
	}
}

// validateCWMPBootstrapStartup validates the deliberately narrow bootstrap
// credential used only for first-contact CWMP enrollment. It is separate from
// ACS_DIGEST_USERNAME/PASSWORD so the legacy fleet-wide compatibility secret
// cannot accidentally become a production onboarding identity.
func validateCWMPBootstrapStartup(logger *slog.Logger) error {
	username := strings.TrimSpace(os.Getenv("ACS_CWMP_BOOTSTRAP_USERNAME"))
	password := os.Getenv("ACS_CWMP_BOOTSTRAP_PASSWORD")
	passwordConfigured := strings.TrimSpace(password) != ""

	if username == "" && !passwordConfigured {
		return nil
	}
	if username == "" || !passwordConfigured {
		return errors.New("ACS_CWMP_BOOTSTRAP_USERNAME and ACS_CWMP_BOOTSTRAP_PASSWORD must be configured together")
	}
	if sharedUsername := strings.TrimSpace(os.Getenv("ACS_DIGEST_USERNAME")); sharedUsername != "" && username == sharedUsername {
		return errors.New("ACS_CWMP_BOOTSTRAP_USERNAME must be distinct from ACS_DIGEST_USERNAME")
	}

	return config.Validate(logger, config.Secret{
		Env:      "ACS_CWMP_BOOTSTRAP_PASSWORD",
		MinBytes: 16,
		Purpose:  "authenticates constrained first-contact CWMP bootstrap sessions",
	})
}

// validateCPEAuthStartup validates the profile, transport configuration and
// secrets used by cmd/acs before it opens the database and constructs the
// Digest authenticator. It is the early startup gate main() already calls, so
// profile mistakes cannot bypass the later per-request production guard.
//
// A fleet-wide Digest password and CWMP mTLS CA are optional authentication
// sources: productionCWMPGuard rejects the shared Digest identity in
// production, while the runtime always installs the device_credentials
// lookup for unique, device-bound Digest credentials. Requiring one of the
// two legacy/global sources here therefore prevented the safest supported
// production posture -- per-device Digest only -- from starting at all.
//
// When the shared Digest password is absent, ACS_CREDENTIAL_ENCRYPTION_KEY
// becomes required. Besides decrypting device_credentials, main.go uses it
// as DigestAuthenticator.NonceSecret in that mode. Requiring it here keeps
// per-device-only Digest nonces keyed with operator-provided secret material
// rather than falling back to the empty shared password.
func validateCPEAuthStartup(logger *slog.Logger, authSecrets ...config.Secret) error {
	if logger == nil {
		logger = slog.Default()
	}

	profile, err := loadACSDeploymentProfile()
	if err != nil {
		return err
	}
	if err := validateCWMPTransportStartup(profile); err != nil {
		return err
	}
	// main.go reads ACS_TLS_MIN_VERSION later while building tls.Config.
	// Normalize the production default here so the existing listener code
	// cannot silently retain its lab-oriented TLS 1.0 fallback.
	if profile == acsDeploymentProfileProduction && strings.TrimSpace(os.Getenv("ACS_TLS_MIN_VERSION")) == "" {
		if err := os.Setenv("ACS_TLS_MIN_VERSION", "1.2"); err != nil {
			return fmt.Errorf("set production TLS minimum: %w", err)
		}
	}

	// The fleet-wide Digest password and mTLS CA are optional sources. If
	// either is configured, still enforce its normal strength/placeholder
	// validation rather than silently accepting a bad value.
	optionalAuthSecrets := append([]config.Secret(nil), authSecrets...)
	for i := range optionalAuthSecrets {
		optionalAuthSecrets[i].Optional = true
	}
	if err := config.Validate(logger, optionalAuthSecrets...); err != nil {
		return err
	}
	if err := validateCWMPBootstrapStartup(logger); err != nil {
		return err
	}

	credentialKey := config.Secret{
		Env:      "ACS_CREDENTIAL_ENCRYPTION_KEY",
		MinBytes: 16,
		Purpose:  "decrypts per-device CWMP Digest credentials and signs Digest nonces when no shared Digest password is configured",
		Optional: strings.TrimSpace(os.Getenv("ACS_DIGEST_PASSWORD")) != "",
	}
	return config.Validate(logger, credentialKey)
}
