package captures

import (
	"encoding/base64"
	"strings"
)

// sensitiveNamePatterns is deliberately a pattern match, not an exact
// allowlist tied to this codebase's known canonical parameters (design
// §6) -- CWMP/USP can target arbitrary vendor-specific paths, and a
// pattern catches those too. Over-redacting something that merely
// contains "password" in its name but isn't actually secret is a cheap
// false positive; under-redacting a real secret is not acceptable.
var sensitiveNamePatterns = []string{
	"password", "passphrase", "secret", "psk", "presharedkey", "privatekey",
}

const redactedMarker = "***REDACTED***"

// RedactParamValue returns value unchanged unless name looks like a
// secret parameter (case-insensitive substring match against
// sensitiveNamePatterns), in which case it returns the fixed marker.
// The name itself is never touched -- an operator can still see which
// parameter was being set, only the value is masked.
func RedactParamValue(name, value string) string {
	lower := strings.ToLower(name)
	for _, pattern := range sensitiveNamePatterns {
		if strings.Contains(lower, pattern) {
			return redactedMarker
		}
	}
	return value
}

// RedactAuthHeader returns a Digest Authorization header verbatim (its
// response field is a one-way hash, not the password, and seeing it is
// the actual diagnostic payload this feature exists to show) but never
// returns a Basic header's credential -- Basic is a trivially-reversible
// base64 encoding of the real credential, so only the username is kept.
func RedactAuthHeader(header string) string {
	if header == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(header, "Basic "); ok {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
		if err != nil {
			return "Basic auth (undecodable)"
		}
		user, _, _ := strings.Cut(string(decoded), ":")
		return "Basic auth, username=" + user
	}
	return header
}
