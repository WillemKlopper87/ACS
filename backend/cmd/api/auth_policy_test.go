package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/operators"
)

// These exercise withJWTAuth's routing rules (audit P1.4): where each
// kind of credential is accepted, and where it is refused.
func TestWithJWTAuth_CredentialPlacement(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	const svc = "service-token-service-token-service-token"
	now := time.Now().UTC()
	session, _ := auth.SignJWT(secret, auth.Claims{Subject: "alice", Role: operators.RoleNOC, IssuedAt: now, ExpiresAt: now.Add(time.Hour)})
	ticket, _ := auth.SignJWT(secret, auth.Claims{Subject: "alice", Role: operators.RoleNOC, Audience: auth.AudienceBrowserTicket, IssuedAt: now, ExpiresAt: now.Add(time.Minute)})

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := withJWTAuth(secret, svc, ok)

	cases := []struct {
		name   string
		method string
		path   string
		header string // bearer value, "" for none
		query  string // ?token= value, "" for none
		want   int
	}{
		{"session token in header on normal route", "GET", "/api/v1/devices", session, "", 200},
		{"session token in query on normal route is refused", "GET", "/api/v1/devices", "", session, 401},
		{"session token in query on WS route is refused (not a ticket)", "GET", "/api/v1/devices/d1/cli/connect", "", session, 401},
		{"ticket in query on WS route", "GET", "/api/v1/devices/d1/cli/connect", "", ticket, 200},
		{"ticket in query on proxy route", "GET", "/api/v1/devices/d1/webgui/proxy/index.html", "", ticket, 200},
		{"ticket in header on normal route is refused", "GET", "/api/v1/devices", ticket, "", 401},
		{"ticket in query on normal route is refused", "GET", "/api/v1/devices", "", ticket, 401},
		{"service token on allowed route", "PUT", "/api/v1/devices/d1/parameters", svc, "", 200},
		{"service token on job status", "GET", "/api/v1/jobs/key-1", svc, "", 200},
		{"service token on device lookup", "GET", "/api/v1/devices/d1", svc, "", 200},
		{"service token on operator management is refused", "POST", "/api/v1/auth/operators", svc, "", 403},
		{"service token on device list is refused", "GET", "/api/v1/devices", svc, "", 403},
		{"service token in query is refused", "GET", "/api/v1/devices/d1/cli/connect", "", svc, 401},
		{"no credential", "GET", "/api/v1/devices", "", "", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := tc.path
			if tc.query != "" {
				url += "?token=" + tc.query
			}
			req := httptest.NewRequest(tc.method, url, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", "Bearer "+tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s %s → %d, want %d", tc.method, url, rec.Code, tc.want)
			}
		})
	}
}
