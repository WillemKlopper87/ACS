package parameters

import "strings"

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
