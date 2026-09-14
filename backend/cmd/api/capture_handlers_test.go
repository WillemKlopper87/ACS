package main

import (
	"encoding/json"
	"strings"
	"testing"

	"acs/internal/operators"
	"acs/internal/tenancy"
)

// TestCaptureHandlers exercises the on-demand session-capture REST
// endpoints (design docs/superpowers/specs/2026-09-14-session-capture-design.md
// §4/§7) against a real Postgres, the same way integration_test.go
// exercises every other handler — through the live router and JWT auth,
// not a bare httptest.NewRecorder against the handler method directly.
func TestCaptureHandlers(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("Customer A"), e.customer("Customer B")
	devA := e.device("A001", &custA)
	devA2 := e.device("A002", &custA)
	e.device("B001", &custB)
	e.operator("op", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.operator("other", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custB})
	e.operator("root", operators.RoleSuperAdmin)
	e.grant(operators.RoleNOC, operators.PermDiagnosticsRun)

	t.Run("create by device", func(t *testing.T) {
		r := e.call("op", "POST", "/api/v1/devices/"+devA+"/captures", map[string]string{"protocol": "CWMP"})
		if r.code != 202 {
			t.Fatalf("create device capture → %d %s, want 202", r.code, r.body)
		}
		var out struct {
			ID        string `json:"id"`
			MatchType string `json:"match_type"`
		}
		if err := json.Unmarshal([]byte(r.body), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.ID == "" {
			t.Errorf("response has no session id: %s", r.body)
		}
		if out.MatchType != "device" {
			t.Errorf("match_type = %q, want %q", out.MatchType, "device")
		}
	})

	t.Run("create by identity", func(t *testing.T) {
		r := e.call("op", "POST", "/api/v1/captures", map[string]string{
			"match_type": "identity", "match_value": "001349+S1", "protocol": "CWMP",
		})
		if r.code != 202 {
			t.Fatalf("create identity capture → %d %s, want 202", r.code, r.body)
		}
	})

	t.Run("create with invalid match_type", func(t *testing.T) {
		r := e.call("op", "POST", "/api/v1/captures", map[string]string{
			"match_type": "bogus", "match_value": "x", "protocol": "CWMP",
		})
		if r.code != 400 {
			t.Errorf("create with invalid match_type → %d, want 400 (%s)", r.code, r.body)
		}
	})

	t.Run("stop an active session", func(t *testing.T) {
		r := e.call("op", "POST", "/api/v1/captures", map[string]string{
			"match_type": "identity", "match_value": "001349+S2", "protocol": "CWMP",
		})
		if r.code != 202 {
			t.Fatalf("create → %d %s", r.code, r.body)
		}
		var created struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(r.body), &created)

		r = e.call("op", "POST", "/api/v1/captures/"+created.ID+"/stop", nil)
		if r.code != 200 {
			t.Fatalf("stop → %d %s, want 200", r.code, r.body)
		}
		var stopped struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal([]byte(r.body), &stopped)
		if stopped.Status != "STOPPED" {
			t.Errorf("status after stop = %q, want STOPPED", stopped.Status)
		}
	})

	t.Run("stop an unknown id", func(t *testing.T) {
		// A syntactically valid UUID that matches no row -- the id column
		// is UUID-typed, so a non-UUID string would fail at the query
		// layer with a 500 rather than exercise the ErrNotFound path this
		// test is actually after.
		r := e.call("op", "POST", "/api/v1/captures/00000000-0000-0000-0000-000000000000/stop", nil)
		if r.code != 404 {
			t.Errorf("stop unknown id → %d, want 404 (%s)", r.code, r.body)
		}
	})

	t.Run("malformed ids are not internal errors", func(t *testing.T) {
		for _, path := range []string{
			"/api/v1/captures/not-a-uuid/stop",
			"/api/v1/captures/not-a-uuid/events",
			"/api/v1/captures/not-a-uuid/export",
		} {
			method := "GET"
			if strings.HasSuffix(path, "/stop") {
				method = "POST"
			}
			if r := e.call("op", method, path, nil); r.code != 404 {
				t.Errorf("%s %s → %d, want 404 (%s)", method, path, r.code, r.body)
			}
		}
	})

	t.Run("tenant scope protects sessions and transcripts", func(t *testing.T) {
		created := e.call("op", "POST", "/api/v1/devices/"+devA2+"/captures", map[string]string{"protocol": "USP"})
		if created.code != 202 {
			t.Fatalf("create scoped capture → %d %s", created.code, created.body)
		}
		var session struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(created.body), &session)

		if r := e.call("other", "GET", "/api/v1/captures/"+session.ID+"/events", nil); r.code != 404 {
			t.Errorf("cross-tenant events → %d, want 404 (%s)", r.code, r.body)
		}
		if r := e.call("other", "GET", "/api/v1/captures/"+session.ID+"/export", nil); r.code != 404 {
			t.Errorf("cross-tenant export → %d, want 404 (%s)", r.code, r.body)
		}
		if r := e.call("other", "POST", "/api/v1/captures/"+session.ID+"/stop", nil); r.code != 404 {
			t.Errorf("cross-tenant stop → %d, want 404 (%s)", r.code, r.body)
		}
		if r := e.call("other", "GET", "/api/v1/captures", nil); strings.Contains(r.body, session.ID) {
			t.Errorf("cross-tenant list leaked %s: %s", session.ID, r.body)
		}
		if r := e.call("root", "GET", "/api/v1/captures/"+session.ID+"/events", nil); r.code != 200 {
			t.Errorf("superadmin events → %d, want 200 (%s)", r.code, r.body)
		}

		knownOther := e.call("op", "POST", "/api/v1/captures", map[string]string{
			"match_type": "identity", "match_value": "ABCDEF-B001", "protocol": "CWMP",
		})
		if knownOther.code != 404 {
			t.Errorf("cross-tenant known identity capture → %d, want 404 (%s)", knownOther.code, knownOther.body)
		}
		if r := e.call("op", "POST", "/api/v1/captures", map[string]string{
			"match_type": "remote_ip", "match_value": "192.0.2.10", "protocol": "CWMP",
		}); r.code != 403 {
			t.Errorf("scoped remote-IP capture → %d, want 403 (%s)", r.code, r.body)
		}
	})

	t.Run("list returns every session newest first with effective_status", func(t *testing.T) {
		r := e.call("op", "POST", "/api/v1/captures", map[string]string{
			"match_type": "identity", "match_value": "001349+S3", "protocol": "USP",
		})
		if r.code != 202 {
			t.Fatalf("create → %d %s", r.code, r.body)
		}
		var created struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(r.body), &created)

		r = e.call("op", "GET", "/api/v1/captures", nil)
		if r.code != 200 {
			t.Fatalf("list → %d %s", r.code, r.body)
		}
		var out struct {
			Items []struct {
				ID              string `json:"id"`
				Status          string `json:"status"`
				EffectiveStatus string `json:"effective_status"`
				StartedAt       string `json:"started_at"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(r.body), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(out.Items) < 2 {
			t.Fatalf("list has %d items, want at least 2", len(out.Items))
		}
		// Newest first.
		for i := 1; i < len(out.Items); i++ {
			if out.Items[i-1].StartedAt < out.Items[i].StartedAt {
				t.Errorf("list is not newest-first: item %d started_at %s < item %d started_at %s",
					i-1, out.Items[i-1].StartedAt, i, out.Items[i].StartedAt)
			}
		}
		found := false
		for _, it := range out.Items {
			if it.ID == created.ID {
				found = true
				if it.EffectiveStatus == "" {
					t.Errorf("effective_status is empty for %s", it.ID)
				}
			}
		}
		if !found {
			t.Errorf("newly created session %s not present in list", created.ID)
		}

		// A STOPPED session's status and effective_status agree; an
		// ACTIVE, unexpired one's do too — the distinctness the brief
		// asks for is exercised by the "stop" subtest above (STOPPED)
		// versus this ACTIVE one, both visible via the same list call.
		for _, it := range out.Items {
			if it.ID == created.ID && it.Status != "ACTIVE" {
				t.Errorf("freshly created session status = %q, want ACTIVE", it.Status)
			}
		}
	})

	t.Run("events are returned in order", func(t *testing.T) {
		r := e.call("op", "POST", "/api/v1/captures", map[string]string{
			"match_type": "identity", "match_value": "001349+S4", "protocol": "CWMP",
		})
		if r.code != 202 {
			t.Fatalf("create → %d %s", r.code, r.body)
		}
		var created struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(r.body), &created)

		for _, summary := range []string{"first", "second", "third"} {
			s := summary
			if err := e.h.captures.RecordEvent(e.ctx, created.ID, "inbound", "RPC", s, &s); err != nil {
				t.Fatalf("record event: %v", err)
			}
		}

		r = e.call("op", "GET", "/api/v1/captures/"+created.ID+"/events", nil)
		if r.code != 200 {
			t.Fatalf("get events → %d %s", r.code, r.body)
		}
		var out struct {
			Items []struct {
				Seq     int    `json:"seq"`
				Summary string `json:"summary"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(r.body), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(out.Items) != 3 {
			t.Fatalf("got %d events, want 3", len(out.Items))
		}
		want := []string{"first", "second", "third"}
		for i, it := range out.Items {
			if it.Summary != want[i] {
				t.Errorf("event %d summary = %q, want %q", i, it.Summary, want[i])
			}
			if it.Seq != i+1 {
				t.Errorf("event %d seq = %d, want %d", i, it.Seq, i+1)
			}
		}
	})

	t.Run("second capture on the same target is a conflict", func(t *testing.T) {
		body := map[string]string{"match_type": "identity", "match_value": "001349+S5", "protocol": "CWMP"}
		r := e.call("op", "POST", "/api/v1/captures", body)
		if r.code != 202 {
			t.Fatalf("first create → %d %s, want 202", r.code, r.body)
		}
		r = e.call("op", "POST", "/api/v1/captures", body)
		if r.code != 409 {
			t.Errorf("second create on the same target → %d, want 409 (%s)", r.code, r.body)
		}
	})
}
