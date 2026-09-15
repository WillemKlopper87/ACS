package main

import (
	"log/slog"
	"os"
	"strings"

	"acs/internal/config"
)

// validateCPEAuthStartup validates the secrets used by cmd/acs before it
// opens the database and constructs the Digest authenticator.
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

	credentialKey := config.Secret{
		Env:      "ACS_CREDENTIAL_ENCRYPTION_KEY",
		MinBytes: 16,
		Purpose:  "decrypts per-device CWMP Digest credentials and signs Digest nonces when no shared Digest password is configured",
		Optional: strings.TrimSpace(os.Getenv("ACS_DIGEST_PASSWORD")) != "",
	}
	return config.Validate(logger, credentialKey)
}
