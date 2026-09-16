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
