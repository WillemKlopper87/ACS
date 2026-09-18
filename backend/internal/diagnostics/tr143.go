// Package diagnostics contains shared validation for resource-intensive CPE
// diagnostics. It deliberately has no network client: the CPE performs the
// transfer, while the ACS validates the requested target before queueing it.
package diagnostics

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateTR143Target accepts only HTTPS URLs whose hostname is explicitly
// allowlisted. Hostnames resolving to loopback, link-local, private, or
// unspecified address ranges are rejected to prevent SSRF through a CPE.
func ValidateTR143Target(raw string, allowedHosts []string) error {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("TR-143 target must be an HTTPS URL without userinfo")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	allowed := false
	for _, candidate := range allowedHosts {
		if strings.EqualFold(host, strings.TrimSuffix(strings.TrimSpace(candidate), ".")) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("TR-143 target host is not allowlisted")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return fmt.Errorf("TR-143 target must not use a private or local address")
	}
	return nil
}
