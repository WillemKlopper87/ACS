package config

import (
	"strings"
	"testing"
)

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

// with returns base overridden by pairs; an empty value unsets.
func with(base []string, pairs ...string) func(string) string {
	return env(append(append([]string{}, base...), pairs...)...)
}

func TestDeploymentProfile(t *testing.T) {
	for in, want := range map[string]string{"": ProfileLab, "lab": ProfileLab, " Production ": ProfileProduction} {
		got, err := DeploymentProfile(env("ACS_DEPLOYMENT_PROFILE", in))
		if err != nil || got != want {
			t.Errorf("profile %q = %q, %v; want %q", in, got, err, want)
		}
	}
	// A typo must not fall back to lab.
	if _, err := DeploymentProfile(env("ACS_DEPLOYMENT_PROFILE", "prodution")); err == nil {
		t.Error("a misspelt profile was accepted")
	}
}

var goodAPI = []string{
	"ACS_CREDENTIAL_ENCRYPTION_KEY", "a-real-key-0123456789",
	"ACS_MFA_REQUIRED_ROLES", "superadmin, manager,noc",
}

func TestAPIProductionProblems(t *testing.T) {
	if p := APIProductionProblems(env(goodAPI...)); len(p) != 0 {
		t.Fatalf("a correct configuration was refused: %v", p)
	}
	if p := APIProductionProblems(with(goodAPI, "ACS_MFA_REQUIRED_ROLES", "all")); len(p) != 0 {
		t.Errorf(`"all" refused: %v`, p)
	}
	cases := map[string]struct {
		env  func(string) string
		want string
	}{
		"no MFA requirement":  {with(goodAPI, "ACS_MFA_REQUIRED_ROLES", ""), "missing: superadmin, manager, noc"},
		"MFA for admins only": {with(goodAPI, "ACS_MFA_REQUIRED_ROLES", "superadmin"), "missing: manager, noc"},
		"no encryption key":   {with(goodAPI, "ACS_CREDENTIAL_ENCRYPTION_KEY", ""), "ACS_CREDENTIAL_ENCRYPTION_KEY"},
		"dev mode":            {with(goodAPI, DevModeEnv, "true"), DevModeEnv},
		"web GUI sandbox off": {with(goodAPI, "ACS_WEBGUI_ALLOW_SAME_ORIGIN", "true"), "ACS_WEBGUI_ALLOW_SAME_ORIGIN"},
		"any CORS origin":     {with(goodAPI, "ACS_API_CORS_ORIGIN", "*"), "ACS_API_CORS_ORIGIN"},
	}
	for name, c := range cases {
		p := APIProductionProblems(c.env)
		if len(p) != 1 || !strings.Contains(p[0], c.want) {
			t.Errorf("%s: problems %v, want one naming %q", name, p, c.want)
		}
	}
}

var goodBSS = []string{
	"ACS_BSS_OAUTH_SIGNING_SECRET", "a-real-signing-secret-0123456789abcdef",
	"ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS", "true",
	"ACS_BSS_ADDR", "127.0.0.1:8090",
	"ACS_BSS_ALLOWED_CIDRS", "198.51.100.0/24, 2001:db8:42::/64",
	"ACS_TRUSTED_PROXY_CIDRS", "127.0.0.1/32",
}

func TestBSSProductionProblems(t *testing.T) {
	if p := BSSProductionProblems(env(goodBSS...)); len(p) != 0 {
		t.Fatalf("a correct configuration was refused: %v", p)
	}
	// Terminating TLS itself on a public address is also acceptable, and
	// then there is no proxy to trust.
	ownTLS := with(goodBSS, "ACS_BSS_ADDR", "0.0.0.0:8443", "ACS_BSS_TLS_CERT", "/etc/acs/bss.crt", "ACS_BSS_TLS_KEY", "/etc/acs/bss.key", "ACS_TRUSTED_PROXY_CIDRS", "")
	if p := BSSProductionProblems(ownTLS); len(p) != 0 {
		t.Errorf("adapter with its own TLS refused: %v", p)
	}

	cases := map[string]struct {
		env  func(string) string
		want string
	}{
		"shared token still set":      {with(goodBSS, "ACS_BSS_API_TOKEN", "legacy-shared-token-0123"), "ACS_BSS_API_TOKEN must be unset"},
		"global clients accepted":     {with(goodBSS, "ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS", ""), "ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS=true"},
		"no OAuth secret":             {with(goodBSS, "ACS_BSS_OAUTH_SIGNING_SECRET", ""), "ACS_BSS_OAUTH_SIGNING_SECRET"},
		"dev mode":                    {with(goodBSS, DevModeEnv, "true"), DevModeEnv},
		"plain HTTP on every address": {with(goodBSS, "ACS_BSS_ADDR", ":8090"), "plain HTTP"},
		"default address":             {with(goodBSS, "ACS_BSS_ADDR", ""), "plain HTTP on :8090"},
		"no source networks":          {with(goodBSS, "ACS_BSS_ALLOWED_CIDRS", ""), "ACS_BSS_ALLOWED_CIDRS is required"},
		"every network allowed":       {with(goodBSS, "ACS_BSS_ALLOWED_CIDRS", "0.0.0.0/0"), "universal network"},
		"every IPv6 network allowed":  {with(goodBSS, "ACS_BSS_ALLOWED_CIDRS", "198.51.100.0/24,::/0"), "universal network"},
		"malformed source network":    {with(goodBSS, "ACS_BSS_ALLOWED_CIDRS", "198.51.100.0"), "invalid CIDR"},
		"proxy not trusted":           {with(goodBSS, "ACS_TRUSTED_PROXY_CIDRS", ""), "ACS_TRUSTED_PROXY_CIDRS is required"},
	}
	for name, c := range cases {
		p := BSSProductionProblems(c.env)
		if len(p) != 1 || !strings.Contains(p[0], c.want) {
			t.Errorf("%s: problems %v, want one naming %q", name, p, c.want)
		}
	}
}

func TestIsLoopbackListener(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8090": true, "localhost:8090": true, "[::1]:8090": true,
		":8090": false, "0.0.0.0:8090": false, "10.0.0.5:8090": false, "nonsense": false,
	} {
		if got := isLoopbackListener(addr); got != want {
			t.Errorf("isLoopbackListener(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestIntegrationMode(t *testing.T) {
	for in, want := range map[string]string{"": ModeNaaS, "naas": ModeNaaS, " Standalone ": ModeStandalone} {
		got, err := IntegrationMode(env("ACS_INTEGRATION_MODE", in))
		if err != nil || got != want {
			t.Errorf("mode %q = %q, %v; want %q", in, got, err, want)
		}
	}
	// A typo must not quietly leave the integration on (or off).
	if _, err := IntegrationMode(env("ACS_INTEGRATION_MODE", "stand-alone")); err == nil {
		t.Error("a misspelt mode was accepted")
	}
}
