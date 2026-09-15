package main

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"acs/internal/auth"
	"acs/internal/config"
)

func testCPEAuthSecrets() []config.Secret {
	return []config.Secret{
		{Env: "ACS_DIGEST_PASSWORD", MinBytes: 16, Purpose: "authenticates CPE CWMP sessions via HTTP Digest"},
		{Env: "ACS_MTLS_CA_CERT", MinBytes: 1, Purpose: "authenticates CPE CWMP sessions via client certificates"},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func clearCPEAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv(config.DevModeEnv, "")
	t.Setenv("ACS_DIGEST_PASSWORD", "")
	t.Setenv("ACS_MTLS_CA_CERT", "")
	t.Setenv("ACS_CREDENTIAL_ENCRYPTION_KEY", "")
}

func TestValidateCPEAuthStartupAllowsPerDeviceDigestOnly(t *testing.T) {
	clearCPEAuthEnv(t)
	t.Setenv("ACS_CREDENTIAL_ENCRYPTION_KEY", "device-credential-key-material-32b")

	if err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...); err != nil {
		t.Fatalf("per-device-only Digest startup rejected: %v", err)
	}
}

func TestValidateCPEAuthStartupRequiresNonceSecretWithoutSharedDigest(t *testing.T) {
	clearCPEAuthEnv(t)

	err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...)
	if err == nil {
		t.Fatal("startup with no shared Digest password and no credential/nonce key succeeded; want fail closed")
	}
	if !strings.Contains(err.Error(), "ACS_CREDENTIAL_ENCRYPTION_KEY") {
		t.Fatalf("error = %q, want ACS_CREDENTIAL_ENCRYPTION_KEY requirement", err)
	}
}

func TestValidateCPEAuthStartupStillValidatesConfiguredSharedSecret(t *testing.T) {
	clearCPEAuthEnv(t)
	t.Setenv("ACS_DIGEST_PASSWORD", "short")

	err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...)
	if err == nil {
		t.Fatal("startup with a configured but weak shared Digest password succeeded; want validation error")
	}
	if !strings.Contains(err.Error(), "ACS_DIGEST_PASSWORD") {
		t.Fatalf("error = %q, want ACS_DIGEST_PASSWORD validation failure", err)
	}
}

func TestPerDeviceOnlyDigestRejectsUnknownCredential(t *testing.T) {
	authr := auth.DigestAuthenticator{
		NonceSecret: []byte("device-credential-key-material-32b"),
		Lookup: func(username string) (string, string, bool) {
			if username == "known-device" {
				return "known-device-password-material", "device-123", true
			}
			return "", "", false
		},
	}
	if !authr.Enabled() {
		t.Fatal("Digest authenticator with per-device Lookup is not enabled")
	}

	req, err := http.NewRequest(http.MethodPost, "http://acs.example/cwmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The exact Digest response is deliberately incomplete: the important
	// regression here is that an unknown username never becomes an
	// unauthenticated compatibility path merely because the shared fleet
	// credential is absent.
	req.Header.Set("Authorization", `Digest username="unknown-device"`)
	ok, _, identity := authr.Verify(req)
	if ok {
		t.Fatal("unknown per-device Digest credential authenticated")
	}
	if identity.BoundDeviceID != "" || identity.Username != "" {
		t.Fatalf("rejected credential returned identity %+v", identity)
	}
}
