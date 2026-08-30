// Package config provides fail-closed startup validation for secrets
// (audit 2026-08-28 P0.1). Every service previously degraded to an
// unauthenticated or plaintext mode with only a log warning when a
// secret was absent; that behavior is now reachable only through the
// explicit ACS_INSECURE_DEV_MODE=true escape hatch. In the default
// (production) mode a missing, placeholder, or too-short secret is a
// startup error, and all problems are reported together so an operator
// can fix the whole environment in one pass.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// DevModeEnv is the single, explicit opt-out from secret enforcement.
const DevModeEnv = "ACS_INSECURE_DEV_MODE"

// InsecureDevMode reports whether the operator has explicitly asked for
// the historical fail-open behavior. Only the literal string "true"
// counts — not "1", not "yes" — so it cannot be enabled by accident.
func InsecureDevMode() bool {
	return os.Getenv(DevModeEnv) == "true"
}

// Secret describes one required (or conditionally required) secret.
type Secret struct {
	Env      string // environment variable name
	MinBytes int    // minimum length in bytes
	Purpose  string // one-line description used in error/summary output
	Optional bool   // when true, validated only if set; absence is allowed
}

// placeholderValues are rejected outright regardless of length: they are
// the strings people type when they intend to come back later and never
// do. Comparison is case-insensitive with '-', '_', and ' ' stripped.
var placeholderValues = map[string]bool{
	"changeme": true, "changethis": true, "changeit": true,
	"secret": true, "mysecret": true, "supersecret": true,
	"password": true, "passw0rd": true, "letmein": true,
	"test": true, "testing": true, "dev": true, "development": true,
	"default": true, "example": true, "sample": true, "placeholder": true,
	"admin": true, "root": true, "todo": true, "fixme": true,
	"123456": true, "12345678": true, "abc123": true, "qwerty": true,
	"xxx": true, "xxxxxxxx": true,
}

func isPlaceholder(v string) bool {
	norm := strings.ToLower(v)
	norm = strings.NewReplacer("-", "", "_", "", " ", "").Replace(norm)
	if placeholderValues[norm] {
		return true
	}
	// A value made of one repeated character (e.g. "aaaaaaaaaaaaaaaa")
	// satisfies any length rule while carrying no entropy at all.
	if len(norm) > 0 && strings.Count(norm, norm[:1]) == len(norm) {
		return true
	}
	return false
}

// checkValue validates a single present value against a Secret's rules.
func checkValue(s Secret, v string) error {
	if isPlaceholder(v) {
		return fmt.Errorf("%s is set to a placeholder value — generate a real secret (e.g. openssl rand -base64 32); it %s", s.Env, s.Purpose)
	}
	if len(v) < s.MinBytes {
		return fmt.Errorf("%s is too short (%d bytes, need at least %d); it %s", s.Env, len(v), s.MinBytes, s.Purpose)
	}
	return nil
}

// Validate enforces the given secret rules. In dev mode it only logs a
// single unmistakable warning and returns nil. Otherwise it returns an
// error joining every violation, suitable for a fatal startup log.
func Validate(logger *slog.Logger, secrets ...Secret) error {
	if InsecureDevMode() {
		logger.Warn("ACS_INSECURE_DEV_MODE=true — secret enforcement is DISABLED. Authentication and encryption may be off entirely. Never run this mode on a reachable network.")
		return nil
	}

	var problems []string
	for _, s := range secrets {
		v := os.Getenv(s.Env)
		if v == "" {
			if s.Optional {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s is required; it %s", s.Env, s.Purpose))
			continue
		}
		if err := checkValue(s, v); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("startup configuration is not production-safe (set %s=true only for isolated local development):\n  - %s",
		DevModeEnv, strings.Join(problems, "\n  - "))
}

// RequireOneOf enforces that at least one of the given secrets is set
// (each present value is still validated). Used where two alternative
// mechanisms exist, e.g. digest credentials vs. mTLS on the CWMP
// listener, or OAuth signing secret vs. legacy shared token on the BSS
// adapter. Returns nil in dev mode.
func RequireOneOf(logger *slog.Logger, purpose string, secrets ...Secret) error {
	if InsecureDevMode() {
		return nil
	}
	var problems []string
	anySet := false
	names := make([]string, 0, len(secrets))
	for _, s := range secrets {
		names = append(names, s.Env)
		v := os.Getenv(s.Env)
		if v == "" {
			continue
		}
		anySet = true
		if err := checkValue(s, v); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if !anySet {
		problems = append(problems, fmt.Sprintf("at least one of %s is required; %s", strings.Join(names, ", "), purpose))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("startup configuration is not production-safe (set %s=true only for isolated local development):\n  - %s",
		DevModeEnv, strings.Join(problems, "\n  - "))
}

// LogSummary emits a redacted one-line-per-secret configuration summary
// so an operator can verify what a process actually loaded without any
// secret material reaching the logs.
func LogSummary(logger *slog.Logger, secrets ...Secret) {
	for _, s := range secrets {
		v := os.Getenv(s.Env)
		if v == "" {
			logger.Info("config", "var", s.Env, "state", "not set")
		} else {
			logger.Info("config", "var", s.Env, "state", fmt.Sprintf("set (%d bytes)", len(v)))
		}
	}
}
