package main

import (
	"log/slog"
	"testing"
)

// mapGetenv builds a getenv func backed by m, so loadConfig can be
// tested without touching the process environment.
func mapGetenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestControllerIDFailsClosed(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"tooShort":    "1234567", // 7 bytes
		"placeholder": "change-me",
		"hasSpace":    "has space",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadConfig(mapGetenv(map[string]string{
				"ACS_USP_CONTROLLER_ID":   id,
				"ACS_USP_ALLOW_PLAINTEXT": "true",
			}), slog.Default())
			if err == nil {
				t.Fatalf("loadConfig() with ACS_USP_CONTROLLER_ID=%q = nil error, want error", id)
			}
		})
	}
}

func TestTLSPairRequired(t *testing.T) {
	_, err := loadConfig(mapGetenv(map[string]string{
		"ACS_USP_CONTROLLER_ID": "ci-controller",
		"ACS_USP_TLS_CERT":      "/tmp/cert.pem",
		// ACS_USP_TLS_KEY deliberately unset
	}), slog.Default())
	if err == nil {
		t.Fatal("loadConfig() with a TLS cert but no key = nil error, want error")
	}

	_, err = loadConfig(mapGetenv(map[string]string{
		"ACS_USP_CONTROLLER_ID": "ci-controller",
		"ACS_USP_TLS_KEY":       "/tmp/key.pem",
		// ACS_USP_TLS_CERT deliberately unset
	}), slog.Default())
	if err == nil {
		t.Fatal("loadConfig() with a TLS key but no cert = nil error, want error")
	}
}

func TestPlaintextRequiresOptIn(t *testing.T) {
	_, err := loadConfig(mapGetenv(map[string]string{
		"ACS_USP_CONTROLLER_ID": "ci-controller",
	}), slog.Default())
	if err == nil {
		t.Fatal("loadConfig() with no TLS and no plaintext opt-in = nil error, want error")
	}

	cfg, err := loadConfig(mapGetenv(map[string]string{
		"ACS_USP_CONTROLLER_ID":   "ci-controller",
		"ACS_USP_ALLOW_PLAINTEXT": "true",
	}), slog.Default())
	if err != nil {
		t.Fatalf("loadConfig() with ACS_USP_ALLOW_PLAINTEXT=true = %v, want nil error", err)
	}
	if !cfg.AllowPlaintext {
		t.Error("cfg.AllowPlaintext = false, want true")
	}
}
