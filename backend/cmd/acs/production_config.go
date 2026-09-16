package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"

	"acs/internal/netguard"
)

type devicePlaneConfig struct {
	Profile                string
	Addr                   string
	TLSCert                string
	TLSKey                 string
	TLSMinVersion          uint16
	AllowBasic             bool
	AllowSharedEstablished bool
	AllowedCIDRs           []*net.IPNet
}

func loadDevicePlaneConfig(getenv func(string) string) (devicePlaneConfig, error) {
	c := devicePlaneConfig{Profile: getenv("ACS_DEPLOYMENT_PROFILE"), Addr: getenv("ACS_ADDR"), TLSCert: getenv("ACS_TLS_CERT"), TLSKey: getenv("ACS_TLS_KEY"), AllowBasic: getenv("ACS_AUTH_ALLOW_BASIC") == "1" || getenv("ACS_AUTH_ALLOW_BASIC") == "true", AllowSharedEstablished: getenv("ACS_CWMP_ALLOW_SHARED_ESTABLISHED") == "1" || getenv("ACS_CWMP_ALLOW_SHARED_ESTABLISHED") == "true"}
	if c.Profile == "" {
		c.Profile = "lab"
	}
	if c.Addr == "" {
		c.Addr = ":7547"
	}
	switch v := getenv("ACS_TLS_MIN_VERSION"); v {
	case "", "1.0":
		c.TLSMinVersion = tls.VersionTLS10
	case "1.1":
		c.TLSMinVersion = tls.VersionTLS11
	case "1.2":
		c.TLSMinVersion = tls.VersionTLS12
	case "1.3":
		c.TLSMinVersion = tls.VersionTLS13
	default:
		return c, fmt.Errorf("invalid ACS_TLS_MIN_VERSION %q", v)
	}
	var err error
	c.AllowedCIDRs, err = netguard.ParseCIDRList(getenv("ACS_CWMP_ALLOWED_CIDRS"))
	if err != nil {
		return c, fmt.Errorf("ACS_CWMP_ALLOWED_CIDRS: %w", err)
	}
	if c.Profile != "lab" && c.Profile != "production" {
		return c, fmt.Errorf("ACS_DEPLOYMENT_PROFILE must be lab or production")
	}
	if c.Profile == "production" {
		if c.TLSCert == "" || c.TLSKey == "" {
			return c, fmt.Errorf("production profile requires ACS_TLS_CERT and ACS_TLS_KEY")
		}
		if c.TLSMinVersion < tls.VersionTLS12 {
			return c, fmt.Errorf("production profile requires ACS_TLS_MIN_VERSION=1.2 or 1.3")
		}
		if c.AllowBasic {
			return c, fmt.Errorf("production profile forbids ACS_AUTH_ALLOW_BASIC")
		}
		if c.AllowSharedEstablished {
			return c, fmt.Errorf("production profile forbids ACS_CWMP_ALLOW_SHARED_ESTABLISHED")
		}
		if len(c.AllowedCIDRs) == 0 {
			return c, fmt.Errorf("production profile requires ACS_CWMP_ALLOWED_CIDRS")
		}
		host, _, err := net.SplitHostPort(c.Addr)
		if err != nil {
			return c, fmt.Errorf("invalid ACS_ADDR: %w", err)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			return c, fmt.Errorf("production profile requires ACS_ADDR on a protected non-wildcard interface")
		}
	}
	return c, nil
}

func restrictRemoteCIDRs(next http.Handler, cidrs []*net.IPNet) http.Handler {
	if len(cidrs) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		allowed := false
		for _, n := range cidrs {
			if ip != nil && n.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
