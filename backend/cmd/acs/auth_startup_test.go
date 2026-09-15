package main

import (
	"io"
	"log/slog"
	"net/http"
	"os"
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
	t.Setenv("ACS_DIGEST_USERNAME", "")
	t.Setenv("ACS_DIGEST_PASSWORD", "")
	t.Setenv("ACS_MTLS_CA_CERT", "")
	t.Setenv("ACS_CREDENTIAL_ENCRYPTION_KEY", "")
	t.Setenv("ACS_CWMP_BOOTSTRAP_USERNAME", "")
	t.Setenv("ACS_CWMP_BOOTSTRAP_PASSWORD", "")
}

func clearCWMPTransportEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ACS_DEPLOYMENT_PROFILE", "")
	t.Setenv("ACS_TLS_CERT", "")
	t.Setenv("ACS_TLS_KEY", "")
	t.Setenv("ACS_TLS_MIN_VERSION", "")
	t.Setenv("ACS_AUTH_ALLOW_BASIC", "")
}

func TestLoadACSDeploymentProfile(t *testing.T) {
	clearCWMPTransportEnv(t)

	profile, err := loadACSDeploymentProfile()
	if err != nil || profile != acsDeploymentProfileLab {
		t.Fatalf("default profile = %q, %v; want lab", profile, err)
	}

	t.Setenv("ACS_DEPLOYMENT_PROFILE", "production")
	profile, err = loadACSDeploymentProfile()
	if err != nil || profile != acsDeploymentProfileProduction {
		t.Fatalf("production profile = %q, %v; want production", profile, err)
	}

	t.Setenv("ACS_DEPLOYMENT_PROFILE", "prodution")
	if _, err := loadACSDeploymentProfile(); err == nil {
		t.Fatal("misspelled production profile succeeded; want fail closed")
	}
}

func TestValidateCWMPTransportStartupProduction(t *testing.T) {
	clearCWMPTransportEnv(t)

	if err := validateCWMPTransportStartup(acsDeploymentProfileLab); err != nil {
		t.Fatalf("lab compatibility config rejected: %v", err)
	}

	t.Run("tls pair required", func(t *testing.T) {
		clearCWMPTransportEnv(t)
		if err := validateCWMPTransportStartup(acsDeploymentProfileProduction); err == nil {
			t.Fatal("production without TLS pair succeeded")
		}
	})

	t.Run("basic forbidden", func(t *testing.T) {
		clearCWMPTransportEnv(t)
		t.Setenv("ACS_TLS_CERT", "/tmp/cert.pem")
		t.Setenv("ACS_TLS_KEY", "/tmp/key.pem")
		t.Setenv("ACS_AUTH_ALLOW_BASIC", "true")
		if err := validateCWMPTransportStartup(acsDeploymentProfileProduction); err == nil {
			t.Fatal("production with HTTP Basic enabled succeeded")
		}
	})

	t.Run("allows implicit tls 1.2 floor", func(t *testing.T) {
		clearCWMPTransportEnv(t)
		t.Setenv("ACS_TLS_CERT", "/tmp/cert.pem")
		t.Setenv("ACS_TLS_KEY", "/tmp/key.pem")
		if err := validateCWMPTransportStartup(acsDeploymentProfileProduction); err != nil {
			t.Fatalf("production with implicit TLS 1.2 floor rejected: %v", err)
		}
	})

	t.Run("legacy tls floor rejected", func(t *testing.T) {
		for _, version := range []string{"1.0", "1.1"} {
			t.Run(version, func(t *testing.T) {
				clearCWMPTransportEnv(t)
				t.Setenv("ACS_TLS_CERT", "/tmp/cert.pem")
				t.Setenv("ACS_TLS_KEY", "/tmp/key.pem")
				t.Setenv("ACS_TLS_MIN_VERSION", version)
				if err := validateCWMPTransportStartup(acsDeploymentProfileProduction); err == nil {
					t.Fatalf("production with TLS %s floor succeeded", version)
				}
			})
		}
	})
}

func TestValidateCPEAuthStartupRejectsUnknownProfile(t *testing.T) {
	clearCPEAuthEnv(t)
	clearCWMPTransportEnv(t)
	t.Setenv("ACS_DEPLOYMENT_PROFILE", "prodution")
	t.Setenv("ACS_CREDENTIAL_ENCRYPTION_KEY", "device-credential-key-material-32b")

	err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...)
	if err == nil || !strings.Contains(err.Error(), "ACS_DEPLOYMENT_PROFILE") {
		t.Fatalf("unknown profile startup error = %v, want deployment-profile failure", err)
	}
}

func TestValidateCPEAuthStartupAppliesProductionTLSDefault(t *testing.T) {
	clearCPEAuthEnv(t)
	clearCWMPTransportEnv(t)
	t.Setenv("ACS_DEPLOYMENT_PROFILE", "production")
	t.Setenv("ACS_TLS_CERT", "/tmp/cert.pem")
	t.Setenv("ACS_TLS_KEY", "/tmp/key.pem")
	// A configured strong shared secret avoids making this transport-default
	// regression depend on the per-device credential-key requirement.
	t.Setenv("ACS_DIGEST_PASSWORD", "production-digest-password-material")

	if err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...); err != nil {
		t.Fatalf("valid production startup rejected: %v", err)
	}
	if got := strings.TrimSpace(os.Getenv("ACS_TLS_MIN_VERSION")); got != "1.2" {
		t.Fatalf("ACS_TLS_MIN_VERSION after production startup = %q, want 1.2", got)
	}
}

func TestValidateCPEAuthStartupAllowsPerDeviceDigestOnly(t *testing.T) {
	clearCPEAuthEnv(t)
	clearCWMPTransportEnv(t)
	t.Setenv("ACS_CREDENTIAL_ENCRYPTION_KEY", "device-credential-key-material-32b")

	if err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...); err != nil {
		t.Fatalf("per-device-only Digest startup rejected: %v", err)
	}
}

func TestValidateCPEAuthStartupRequiresNonceSecretWithoutSharedDigest(t *testing.T) {
	clearCPEAuthEnv(t)
	clearCWMPTransportEnv(t)

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
	clearCWMPTransportEnv(t)
	t.Setenv("ACS_DIGEST_PASSWORD", "short")

	err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...)
	if err == nil {
		t.Fatal("startup with a configured but weak shared Digest password succeeded; want validation error")
	}
	if !strings.Contains(err.Error(), "ACS_DIGEST_PASSWORD") {
		t.Fatalf("error = %q, want ACS_DIGEST_PASSWORD validation failure", err)
	}
}

func TestValidateCPEAuthStartupBootstrapScope(t *testing.T) {
	t.Run("strong explicit bootstrap pair accepted", func(t *testing.T) {
		clearCPEAuthEnv(t)
		clearCWMPTransportEnv(t)
		t.Setenv("ACS_CREDENTIAL_ENCRYPTION_KEY", "device-credential-key-material-32b")
		t.Setenv("ACS_CWMP_BOOTSTRAP_USERNAME", "cwmp-bootstrap")
		t.Setenv("ACS_CWMP_BOOTSTRAP_PASSWORD", "bootstrap-password-material-32b")

		if err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...); err != nil {
			t.Fatalf("valid bootstrap configuration rejected: %v", err)
		}
	})

	t.Run("username without password fails closed", func(t *testing.T) {
		clearCPEAuthEnv(t)
		clearCWMPTransportEnv(t)
		t.Setenv("ACS_CREDENTIAL_ENCRYPTION_KEY", "device-credential-key-material-32b")
		t.Setenv("ACS_CWMP_BOOTSTRAP_USERNAME", "cwmp-bootstrap")

		err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...)
		if err == nil || !strings.Contains(err.Error(), "ACS_CWMP_BOOTSTRAP") {
			t.Fatalf("partial bootstrap configuration error = %v, want fail closed", err)
		}
	})

	t.Run("password without username fails closed", func(t *testing.T) {
		clearCPEAuthEnv(t)
		clearCWMPTransportEnv(t)
		t.Setenv("ACS_CREDENTIAL_ENCRYPTION_KEY", "device-credential-key-material-32b")
		t.Setenv("ACS_CWMP_BOOTSTRAP_PASSWORD", "bootstrap-password-material-32b")

		err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...)
		if err == nil || !strings.Contains(err.Error(), "ACS_CWMP_BOOTSTRAP") {
			t.Fatalf("partial bootstrap configuration error = %v, want fail closed", err)
		}
	})

	t.Run("weak bootstrap password rejected", func(t *testing.T) {
		clearCPEAuthEnv(t)
		clearCWMPTransportEnv(t)
		t.Setenv("ACS_CREDENTIAL_ENCRYPTION_KEY", "device-credential-key-material-32b")
		t.Setenv("ACS_CWMP_BOOTSTRAP_USERNAME", "cwmp-bootstrap")
		t.Setenv("ACS_CWMP_BOOTSTRAP_PASSWORD", "short")

		err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...)
		if err == nil || !strings.Contains(err.Error(), "ACS_CWMP_BOOTSTRAP_PASSWORD") {
			t.Fatalf("weak bootstrap password error = %v, want secret validation failure", err)
		}
	})

	t.Run("bootstrap and legacy fleet usernames must differ", func(t *testing.T) {
		clearCPEAuthEnv(t)
		clearCWMPTransportEnv(t)
		t.Setenv("ACS_DIGEST_USERNAME", "shared-name")
		t.Setenv("ACS_DIGEST_PASSWORD", "shared-password-material-32b")
		t.Setenv("ACS_CWMP_BOOTSTRAP_USERNAME", "shared-name")
		t.Setenv("ACS_CWMP_BOOTSTRAP_PASSWORD", "bootstrap-password-material-32b")

		err := validateCPEAuthStartup(discardLogger(), testCPEAuthSecrets()...)
		if err == nil || !strings.Contains(err.Error(), "must be distinct") {
			t.Fatalf("scope-collision error = %v, want distinct username failure", err)
		}
	})
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
