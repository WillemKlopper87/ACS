package main

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"acs/internal/usp"
)

// controllerIDPattern is the shape ACS_USP_CONTROLLER_ID must take: safe
// to embed in a topic name (MQTT) and a URL query parameter (WebSocket
// eid) without further escaping.
var controllerIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// minControllerIDBytes keeps a careless one-character id ("c") from
// becoming this controller's permanent identity: an agent's controller
// table stores the resulting endpoint id verbatim (see main.go's
// package doc), so this is enforced at startup rather than left to a
// convention nobody checks.
const minControllerIDBytes = 8

// placeholderControllerIDs are rejected outright regardless of shape or
// length -- the strings an operator types when they mean to come back
// and set a real value later, and then don't. Comparison is
// case-insensitive with '-', '_', and '.' stripped, mirroring
// internal/config's placeholder check.
var placeholderControllerIDs = map[string]bool{
	"changeme": true, "changethis": true, "changeit": true,
	"test": true, "testing": true, "dev": true, "development": true,
	"default": true, "example": true, "sample": true, "placeholder": true,
	"controller": true, "myacs": true, "acs": true, "uspc": true,
}

// serviceConfig is cmd/uspc's parsed, validated startup configuration.
// Named serviceConfig, not config, because internal/config is imported
// by name into main.go's scope.
type serviceConfig struct {
	// ControllerID is this controller's own USP endpoint id
	// (self::<ACS_USP_CONTROLLER_ID>), stamped as From on every record
	// this process sends and checked as To on every record it accepts.
	ControllerID usp.EndpointID

	WSAddr string
	WSPath string

	MQTTAddr            string
	MQTTControllerTopic string

	// TLSCert and TLSKey are either both empty (plaintext, requires
	// AllowPlaintext) or both set (both transports serve TLS).
	TLSCert        string
	TLSKey         string
	AllowPlaintext bool

	HTTPAddr string
}

// loadConfig reads and validates cmd/uspc's configuration via getenv,
// rather than the process environment directly, so it is testable
// without os.Setenv. Every rule is fail-closed: a bad or missing value
// is an error, never a silently-applied default (defaults exist only
// for genuinely optional knobs -- listen addresses and paths).
func loadConfig(getenv func(string) string, log *slog.Logger) (serviceConfig, error) {
	var problems []string

	rawID := getenv("ACS_USP_CONTROLLER_ID")
	if err := validateControllerID(rawID); err != nil {
		problems = append(problems, err.Error())
	}

	tlsCert := getenv("ACS_USP_TLS_CERT")
	tlsKey := getenv("ACS_USP_TLS_KEY")
	if (tlsCert == "") != (tlsKey == "") {
		problems = append(problems, "ACS_USP_TLS_CERT and ACS_USP_TLS_KEY must both be set or both be empty")
	}

	allowPlaintext := getenv("ACS_USP_ALLOW_PLAINTEXT") == "true"
	if tlsCert == "" && tlsKey == "" && !allowPlaintext {
		problems = append(problems, "no TLS certificate is configured (ACS_USP_TLS_CERT/ACS_USP_TLS_KEY) and ACS_USP_ALLOW_PLAINTEXT is not \"true\" -- refusing to serve USP in plaintext")
	}

	if len(problems) > 0 {
		return serviceConfig{}, fmt.Errorf("uspc: invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}

	return serviceConfig{
		ControllerID: usp.FormatEndpointID("self", rawID),

		WSAddr: envOrDefault(getenv, "ACS_USP_WS_ADDR", ":9877"),
		WSPath: envOrDefault(getenv, "ACS_USP_WS_PATH", "/usp"),

		MQTTAddr:            envOrDefault(getenv, "ACS_USP_MQTT_ADDR", ":1883"),
		MQTTControllerTopic: envOrDefault(getenv, "ACS_USP_MQTT_CONTROLLER_TOPIC", "/usp/controller"),

		TLSCert:        tlsCert,
		TLSKey:         tlsKey,
		AllowPlaintext: allowPlaintext,

		HTTPAddr: envOrDefault(getenv, "ACS_USP_HTTP_ADDR", ":8092"),
	}, nil
}

// validateControllerID enforces the ACS_USP_CONTROLLER_ID rules: present,
// long enough to not be a careless guess, safe to embed unescaped in a
// topic/query-parameter, and not a value that reads as a placeholder.
func validateControllerID(id string) error {
	if id == "" {
		return errors.New("ACS_USP_CONTROLLER_ID is required")
	}
	if len(id) < minControllerIDBytes {
		return fmt.Errorf("ACS_USP_CONTROLLER_ID is too short (%d bytes, need at least %d)", len(id), minControllerIDBytes)
	}
	if !controllerIDPattern.MatchString(id) {
		return fmt.Errorf("ACS_USP_CONTROLLER_ID %q does not match %s", id, controllerIDPattern.String())
	}
	if isPlaceholderControllerID(id) {
		return fmt.Errorf("ACS_USP_CONTROLLER_ID is set to a placeholder value (%q) -- choose a real, stable identifier", id)
	}
	return nil
}

func isPlaceholderControllerID(id string) bool {
	norm := strings.ToLower(id)
	norm = strings.NewReplacer("-", "", "_", "", ".", "").Replace(norm)
	return placeholderControllerIDs[norm]
}

// envOrDefault returns getenv(key), or fallback when that is empty.
func envOrDefault(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
