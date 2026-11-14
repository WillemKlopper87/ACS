package config

import (
	"fmt"
	"net"
	"strings"

	"acs/internal/netguard"
)

// The deployment profile (ACS_DEPLOYMENT_PROFILE). cmd/acs and cmd/uspc
// already refuse lab-only transport settings under "production"; the
// functions here do the same for the operator API and the BSS adapter, so
// that the strict settings are a property of the profile and cannot be
// forgotten on one host (docs/SECURITY-ASSESSMENT-2026-11-14.md, O1).
const (
	ProfileLab        = "lab"
	ProfileProduction = "production"
)

// DeploymentProfile reads the profile. Unset means lab. An unknown value
// is an error rather than lab: a misspelt "production" must not silently
// run with the compatibility settings.
func DeploymentProfile(getenv func(string) string) (string, error) {
	profile := strings.ToLower(strings.TrimSpace(getenv("ACS_DEPLOYMENT_PROFILE")))
	switch profile {
	case "":
		return ProfileLab, nil
	case ProfileLab, ProfileProduction:
		return profile, nil
	default:
		return "", fmt.Errorf("ACS_DEPLOYMENT_PROFILE must be %q or %q, got %q", ProfileLab, ProfileProduction, profile)
	}
}

// The integration mode (ACS_INTEGRATION_MODE) is the switch between the
// ACS running on its own and the ACS run through the ISP NaaS platform.
//
//   - "naas" (the default, and what every deployment did before the switch
//     existed): the BSS adapter serves /bss/v1 and the TMF APIs, and the
//     operator API accepts the adapter's service token.
//   - "standalone": the ACS is run from its own console only. The adapter
//     answers its integration routes with 503 and runs no workers, the
//     operator API accepts no service token, the production profile asks
//     for none of the adapter's settings, and the console hides the
//     BSS / NaaS screen. Nothing is deleted: accounts, mappings and
//     OAuth clients stay in the database and work again when the mode is
//     switched back.
const (
	ModeNaaS       = "naas"
	ModeStandalone = "standalone"
)

// IntegrationMode reads the mode. Unset means naas, so existing
// deployments behave as before. An unknown value is an error.
func IntegrationMode(getenv func(string) string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(getenv("ACS_INTEGRATION_MODE")))
	switch mode {
	case "":
		return ModeNaaS, nil
	case ModeNaaS, ModeStandalone:
		return mode, nil
	default:
		return "", fmt.Errorf("ACS_INTEGRATION_MODE must be %q or %q, got %q", ModeNaaS, ModeStandalone, mode)
	}
}

// writeRoles are the operator roles that can change devices. Production
// requires a second sign-in step for all of them.
var writeRoles = []string{"superadmin", "manager", "noc"}

// APIProductionProblems lists what stops cmd/api from running under the
// production profile. Empty means it may start.
func APIProductionProblems(getenv func(string) string) []string {
	var problems []string
	if getenv(DevModeEnv) == "true" {
		problems = append(problems, DevModeEnv+"=true is forbidden in production: it turns secret enforcement off")
	}
	if strings.TrimSpace(getenv("ACS_CREDENTIAL_ENCRYPTION_KEY")) == "" {
		problems = append(problems, "ACS_CREDENTIAL_ENCRYPTION_KEY is required in production: device credentials and two-step sign-in secrets are sealed with it")
	}

	required := map[string]bool{}
	for _, role := range strings.Split(getenv("ACS_MFA_REQUIRED_ROLES"), ",") {
		required[strings.ToLower(strings.TrimSpace(role))] = true
	}
	if !required["all"] {
		var missing []string
		for _, role := range writeRoles {
			if !required[role] {
				missing = append(missing, role)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf(
				"ACS_MFA_REQUIRED_ROLES must require two-step sign-in for every role that can change devices; missing: %s (set it to %q or \"all\")",
				strings.Join(missing, ", "), strings.Join(writeRoles, ",")))
		}
	}

	if getenv("ACS_WEBGUI_ALLOW_SAME_ORIGIN") == "true" {
		problems = append(problems, "ACS_WEBGUI_ALLOW_SAME_ORIGIN=true is forbidden in production: it lets a device's web pages run on the console's origin")
	}
	return problems
}

// BSSProductionProblems lists what stops cmd/bssadapter from running under
// the production profile. Empty means it may start.
func BSSProductionProblems(getenv func(string) string) []string {
	var problems []string
	if getenv(DevModeEnv) == "true" {
		problems = append(problems, DevModeEnv+"=true is forbidden in production: it turns authentication off")
	}
	if strings.TrimSpace(getenv("ACS_BSS_OAUTH_SIGNING_SECRET")) == "" {
		problems = append(problems, "ACS_BSS_OAUTH_SIGNING_SECRET is required in production: integrations authenticate as OAuth2 clients")
	}
	if getenv("ACS_BSS_API_TOKEN") != "" {
		problems = append(problems, "ACS_BSS_API_TOKEN must be unset in production: the shared token grants every scope on every account and cannot be traced to an integration")
	}
	if getenv("ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS") != "true" {
		problems = append(problems, "ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS=true is required in production: every integration must be bound to one customer")
	}

	// Transport: the adapter terminates TLS itself, or it listens on
	// loopback behind a proxy that does.
	ownTLS := strings.TrimSpace(getenv("ACS_BSS_TLS_CERT")) != "" && strings.TrimSpace(getenv("ACS_BSS_TLS_KEY")) != ""
	addr := getenv("ACS_BSS_ADDR")
	if addr == "" {
		addr = ":8090"
	}
	loopback := isLoopbackListener(addr)
	if !ownTLS && !loopback {
		problems = append(problems, fmt.Sprintf(
			"the adapter would serve plain HTTP on %s: set ACS_BSS_TLS_CERT and ACS_BSS_TLS_KEY, or bind ACS_BSS_ADDR to loopback behind a TLS-terminating proxy", addr))
	}

	// Source networks: only the integration platform's addresses.
	nets, err := netguard.ParseCIDRList(strings.ReplaceAll(getenv("ACS_BSS_ALLOWED_CIDRS"), " ", ""))
	switch {
	case err != nil:
		problems = append(problems, "ACS_BSS_ALLOWED_CIDRS: "+err.Error())
	case len(nets) == 0:
		problems = append(problems, "ACS_BSS_ALLOWED_CIDRS is required in production: list the networks the BSS/NaaS platform calls from")
	default:
		for _, n := range nets {
			if ones, _ := n.Mask.Size(); ones == 0 {
				problems = append(problems, fmt.Sprintf("ACS_BSS_ALLOWED_CIDRS must not contain the universal network %s", n))
			}
		}
	}
	// Behind a proxy every caller arrives from the proxy's address unless
	// the proxy is trusted to say who the caller was.
	if loopback && !ownTLS && strings.TrimSpace(getenv("ACS_TRUSTED_PROXY_CIDRS")) == "" {
		problems = append(problems, "ACS_TRUSTED_PROXY_CIDRS is required when the adapter listens on loopback behind a proxy: without it every caller appears to be the proxy and ACS_BSS_ALLOWED_CIDRS cannot be applied")
	}
	return problems
}

// isLoopbackListener reports whether a listen address accepts connections
// from this host only. ":8090" and "0.0.0.0:8090" do not.
func isLoopbackListener(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// FormatProblems renders a refusal to start.
func FormatProblems(service string, problems []string) string {
	return fmt.Sprintf("%s cannot start with ACS_DEPLOYMENT_PROFILE=production:\n  - %s", service, strings.Join(problems, "\n  - "))
}

// StandaloneHint is appended to the adapter's production refusal: if the
// NaaS integration is not in use, the answer is the switch, not the list.
const StandaloneHint = "If this ACS is not connected to the NaaS platform, set ACS_INTEGRATION_MODE=standalone instead: none of the settings above are then needed."
