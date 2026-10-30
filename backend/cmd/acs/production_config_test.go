package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func validProductionEnv() map[string]string {
	return map[string]string{"ACS_DEPLOYMENT_PROFILE": "production", "ACS_ADDR": "10.0.0.5:7547", "ACS_TLS_CERT": "cert.pem", "ACS_TLS_KEY": "key.pem", "ACS_TLS_MIN_VERSION": "1.2", "ACS_CWMP_ALLOWED_CIDRS": "10.0.0.0/8"}
}

func TestProductionDevicePlaneFailsClosed(t *testing.T) {
	for _, key := range []string{"ACS_TLS_CERT", "ACS_CWMP_ALLOWED_CIDRS"} {
		t.Run(key, func(t *testing.T) {
			e := validProductionEnv()
			delete(e, key)
			if _, err := loadDevicePlaneConfig(envMap(e)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	e := validProductionEnv()
	e["ACS_ADDR"] = ":7547"
	if _, err := loadDevicePlaneConfig(envMap(e)); err == nil {
		t.Fatal("wildcard bind accepted")
	}
	e = validProductionEnv()
	e["ACS_AUTH_ALLOW_BASIC"] = "true"
	if _, err := loadDevicePlaneConfig(envMap(e)); err == nil {
		t.Fatal("Basic auth accepted")
	}
	e = validProductionEnv()
	if _, err := loadDevicePlaneConfig(envMap(e)); err != nil {
		t.Fatal(err)
	}
}

func TestRestrictRemoteCIDRs(t *testing.T) {
	cfg, err := loadDevicePlaneConfig(envMap(validProductionEnv()))
	if err != nil {
		t.Fatal(err)
	}
	h := restrictRemoteCIDRs(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), cfg.AllowedCIDRs)
	for _, tc := range []struct {
		remote string
		want   int
	}{{"10.1.2.3:99", 204}, {"203.0.113.2:99", 403}, {"bad", 403}} {
		r := httptest.NewRequest("GET", "/cwmp", nil)
		r.RemoteAddr = tc.remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s got %d want %d", tc.remote, w.Code, tc.want)
		}
	}
}

// TestLabCIDRAllowlistEnforced pins that ACS_CWMP_ALLOWED_CIDRS applies in
// the lab profile whenever it is set, not only in production (handoff plan
// 2026-10-30 §3a.2: defence in depth if the security group is widened).
func TestLabCIDRAllowlistEnforced(t *testing.T) {
	cfg, err := loadDevicePlaneConfig(envMap(map[string]string{"ACS_CWMP_ALLOWED_CIDRS": "66.18.65.65/32"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != "lab" || len(cfg.AllowedCIDRs) != 1 {
		t.Fatalf("lab config = %+v, want one allowed CIDR", cfg)
	}
	h := restrictRemoteCIDRs(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), cfg.AllowedCIDRs)
	for remote, want := range map[string]int{"66.18.65.65:4000": 204, "66.18.65.66:4000": 403} {
		r := httptest.NewRequest("POST", "/cwmp", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s got %d want %d", remote, w.Code, want)
		}
	}
}

// TestSecondTLSListener: ACS_TLS_ADDR adds a TLS listener beside the plain
// one on ACS_ADDR, so a CPE can be moved to HTTPS while HTTP stays up as
// the recovery path (handoff plan 2026-10-30 §3a.3). Lab only.
func TestSecondTLSListener(t *testing.T) {
	lab := map[string]string{"ACS_ADDR": ":7547", "ACS_TLS_ADDR": ":7548", "ACS_TLS_CERT": "cert.pem", "ACS_TLS_KEY": "key.pem"}
	cfg, err := loadDevicePlaneConfig(envMap(lab))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSAddr != ":7548" || cfg.Addr != ":7547" {
		t.Fatalf("addrs = %q / %q", cfg.Addr, cfg.TLSAddr)
	}
	for name, mutate := range map[string]func(map[string]string){
		"no cert":      func(e map[string]string) { delete(e, "ACS_TLS_CERT") },
		"same address": func(e map[string]string) { e["ACS_TLS_ADDR"] = ":7547" },
	} {
		e := map[string]string{}
		for k, v := range lab {
			e[k] = v
		}
		mutate(e)
		if _, err := loadDevicePlaneConfig(envMap(e)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	prod := validProductionEnv()
	prod["ACS_TLS_ADDR"] = "10.0.0.5:7548"
	if _, err := loadDevicePlaneConfig(envMap(prod)); err == nil {
		t.Fatal("production accepted a second listener that keeps plaintext on ACS_ADDR")
	}
}
