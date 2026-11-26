package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"acs/internal/operators"
	"acs/internal/simwatch"
	"acs/internal/simwatch/recorder"
	"acs/internal/tenancy"
)

var (
	swOld = simwatch.Identity{ICCID: "8927000000000000011", IMSI: "655010000000011"}
	swNew = simwatch.Identity{ICCID: "8927000000000000022", IMSI: "655010000000022"}
)

func TestIntegration_SIMWatch(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("SW A"), e.customer("SW B")
	dev, other := e.device("SW0001A", &custA), e.device("SW0002B", &custB)
	e.operator("alice", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.operator("ro", operators.RoleReadOnly, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	rec := recorder.New(e.db)
	approve := func(as, id string, body any) resp {
		return e.call(as, "POST", "/api/v1/devices/"+id+"/sim-watch/approve", body)
	}
	var seen string // the detection time the operator was shown
	reason := func(r string) map[string]string { return map[string]string{"reason": r, "detected_at": seen} }

	t.Run("nothing to approve is 409", func(t *testing.T) {
		seen = time.Now().UTC().Format(time.RFC3339Nano)
		if got := approve("alice", dev, reason("Customer swapped the SIM on site")); got.code != 409 {
			t.Fatalf("code = %d %s, want 409", got.code, got.body)
		}
	})

	if err := rec.Record(e.ctx, dev, simwatch.Verdict{Kind: simwatch.KindNotice, Network: "65501"}, swNew, swOld, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	t.Run("the SIM view carries the state with masked hints only", func(t *testing.T) {
		got := e.call("alice", "GET", "/api/v1/devices/"+dev+"/sim", nil)
		if got.code != 200 {
			t.Fatalf("code = %d %s", got.code, got.body)
		}
		var v struct {
			SimWatch *struct {
				State      string `json:"state"`
				Network    string `json:"network"`
				ICCIDHint  string `json:"iccid_hint"`
				DetectedAt string `json:"detected_at"`
			} `json:"sim_watch"`
		}
		if err := json.Unmarshal([]byte(got.body), &v); err != nil {
			t.Fatal(err)
		}
		if v.SimWatch == nil || v.SimWatch.State != "notice" || v.SimWatch.Network != "65501" || v.SimWatch.ICCIDHint == "" {
			t.Fatalf("sim_watch = %+v (%s)", v.SimWatch, got.body)
		}
		seen = v.SimWatch.DetectedAt
		for _, full := range []string{swOld.ICCID, swNew.ICCID, swOld.IMSI, swNew.IMSI} {
			if strings.Contains(got.body, full) {
				t.Fatalf("body carries a full identifier: %s", got.body)
			}
		}
	})
	t.Run("a device without a row has no sim_watch", func(t *testing.T) {
		e.operator("bob", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custB})
		g := e.call("bob", "GET", "/api/v1/devices/"+other+"/sim", nil)
		if g.code != 200 || strings.Contains(g.body, "sim_watch") {
			t.Fatalf("code = %d body = %s", g.code, g.body)
		}
	})
	t.Run("reasons that are too short or blank are 400", func(t *testing.T) {
		for _, r := range []string{"", "   ", "short", "         x         "} {
			if got := approve("alice", dev, reason(r)); got.code != 400 {
				t.Errorf("reason %q = %d, want 400", r, got.code)
			}
		}
	})
	t.Run("a read-only operator is refused", func(t *testing.T) {
		if got := approve("ro", dev, reason("Customer swapped the SIM on site")); got.code != 403 {
			t.Fatalf("code = %d, want 403", got.code)
		}
	})
	t.Run("another tenant's device is 404", func(t *testing.T) {
		if got := approve("alice", other, reason("Customer swapped the SIM on site")); got.code != 404 {
			t.Fatalf("code = %d, want 404", got.code)
		}
	})
	t.Run("an audit failure refuses the approval and leaves the state", func(t *testing.T) {
		if _, err := e.db.ExecContext(e.ctx, `ALTER TABLE audit_log RENAME TO audit_log_off`); err != nil {
			t.Fatal(err)
		}
		got := approve("alice", dev, reason("Customer swapped the SIM on site"))
		if _, err := e.db.ExecContext(e.ctx, `ALTER TABLE audit_log_off RENAME TO audit_log`); err != nil {
			t.Fatal(err)
		}
		if got.code != 503 {
			t.Fatalf("code = %d %s, want 503", got.code, got.body)
		}
		var state string
		if err := e.db.QueryRowContext(e.ctx, `SELECT state FROM device_sim_watch WHERE device_id=$1`, dev).Scan(&state); err != nil || state != "notice" {
			t.Fatalf("state = %q, %v; want notice", state, err)
		}
	})
	t.Run("a missing or unparsable detected_at is 400", func(t *testing.T) {
		for _, d := range []string{"", "yesterday"} {
			if got := approve("alice", dev, map[string]string{"reason": "Customer swapped the SIM on site", "detected_at": d}); got.code != 400 {
				t.Errorf("detected_at %q = %d, want 400", d, got.code)
			}
		}
	})
	t.Run("a SIM that changed again since it was shown is not approved", func(t *testing.T) {
		stale := map[string]string{"reason": "Customer swapped the SIM on site", "detected_at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)}
		if got := approve("alice", dev, stale); got.code != 409 {
			t.Fatalf("code = %d %s, want 409", got.code, got.body)
		}
		var state string
		if err := e.db.QueryRowContext(e.ctx, `SELECT state FROM device_sim_watch WHERE device_id=$1`, dev).Scan(&state); err != nil || state != "notice" {
			t.Fatalf("state = %q, %v; want notice", state, err)
		}
	})
	t.Run("approval returns the state to ok and is audited without identifiers", func(t *testing.T) {
		got := approve("alice", dev, reason("  Customer swapped the SIM on site  "))
		if got.code != 200 || !strings.Contains(got.body, `"state":"ok"`) || !strings.Contains(got.body, `"approved_by":"alice"`) {
			t.Fatalf("code = %d body = %s", got.code, got.body)
		}
		var details string
		if err := e.db.QueryRowContext(e.ctx, `SELECT details::text FROM audit_log WHERE action='SimChangeApproved' AND device_id=$1`, dev).Scan(&details); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(details, "Customer swapped the SIM on site") || strings.Contains(details, "0022") || strings.Contains(details, "0011") {
			t.Fatalf("audit details = %s", details)
		}
		if again := approve("alice", dev, reason("Customer swapped the SIM on site")); again.code != 409 {
			t.Fatalf("second approval = %d, want 409", again.code)
		}
	})
}
