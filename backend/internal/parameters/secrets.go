package parameters

import (
	"encoding/json"
	"strings"
)

// SecretRedacted replaces a secret's value in the cache and its history.
const SecretRedacted = "***REDACTED***"

// secretNamePatterns matches secret parameters by name, the same pattern
// list packet captures redact with (internal/captures): CWMP/USP reach
// arbitrary vendor paths, so a substring match catches e.g.
// Device.WiFi.AccessPoint.1.Security.KeyPassphrase,
// ManagementServer.ConnectionRequestPassword and
// XMPP.Connection.1.Password. Over-redacting a non-secret is cheap;
// storing a secret is not.
var secretNamePatterns = []string{"password", "passphrase", "secret", "psk", "presharedkey", "privatekey"}

// IsSecretParameter reports whether a parameter's value must never be
// stored or returned.
func IsSecretParameter(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range secretNamePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// redactSecrets returns values with every non-empty secret replaced by
// SecretRedacted. Before this, a Wi-Fi passphrase or Connection Request
// password the ACS set (SPV values are cached as SET_PARAMETER_VALUES) was
// stored in plain text in device_parameter_cache and parameter_history and
// returned by GET /parameters to every role. An empty value (what CPEs
// return on GPV for passwords) stays empty so "not set" remains visible.
func redactSecrets(values map[string]CachedValue) map[string]CachedValue {
	var out map[string]CachedValue
	for name, v := range values {
		if v.Value == "" || v.Value == SecretRedacted || !IsSecretParameter(name) {
			continue
		}
		if out == nil {
			out = make(map[string]CachedValue, len(values))
			for k, kv := range values {
				out[k] = kv
			}
		}
		v.Value = SecretRedacted
		out[name] = v
	}
	if out == nil {
		return values
	}
	return out
}

// RedactPayloadSecrets returns a job or schedule payload with every secret
// value replaced by SecretRedacted: the value of any {"name": <secret
// parameter>, "value": ...} entry (SetParameterValues writes) and any field
// whose own key is secret-named (Download/Upload "password"). Payloads keep
// the plain values the CPE needs until the RPC is sent; this is for what the
// API returns and what is kept after the job finishes. A payload with
// nothing to redact, or that is not JSON, is returned unchanged.
func RedactPayloadSecrets(payload []byte) []byte {
	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return payload
	}
	if !redactJSONSecrets(doc) {
		return payload
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return payload
	}
	return out
}

func redactJSONSecrets(node any) bool {
	changed := false
	switch n := node.(type) {
	case map[string]any:
		if name, ok := n["name"].(string); ok && IsSecretParameter(name) {
			if v, ok := n["value"].(string); ok && v != "" && v != SecretRedacted {
				n["value"] = SecretRedacted
				changed = true
			}
		}
		for k, v := range n {
			if s, ok := v.(string); ok {
				if s != "" && s != SecretRedacted && IsSecretParameter(k) {
					n[k] = SecretRedacted
					changed = true
				}
				continue
			}
			if redactJSONSecrets(v) {
				changed = true
			}
		}
	case []any:
		for _, v := range n {
			if redactJSONSecrets(v) {
				changed = true
			}
		}
	}
	return changed
}
