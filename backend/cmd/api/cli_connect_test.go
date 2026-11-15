package main

import (
	"encoding/json"
	"testing"

	"acs/internal/cliaccess"
)

// The console WebSocket takes a credential ID. A missing or malformed
// one used to be a 500, and a credential of another device was accepted
// although the browser ticket names one device.
func TestCLIConnectChecksTheCredential(t *testing.T) {
	e := newTestEnv(t)
	e.operator("alice", "superadmin")
	devA, devB := e.device("CLI0001", nil), e.device("CLI0002", nil)
	credB, err := e.h.cli.Create(e.ctx, devB, cliaccess.ProtocolSSH, "192.0.2.10", 22, "admin", "pw")
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	r := e.call("alice", "POST", "/api/v1/auth/ticket", map[string]string{"device_id": devA, "purpose": "cli"})
	if r.code != 200 {
		t.Fatalf("mint ticket → %d %s", r.code, r.body)
	}
	var out struct {
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal([]byte(r.body), &out)
	base := "/api/v1/devices/" + devA + "/cli/connect?token=" + out.Ticket

	for name, c := range map[string]struct {
		query string
		want  int
	}{
		"no credential_id":                {"", 400},
		"credential_id that is not an ID": {"&credential_id=x", 400},
		"unknown credential":              {"&credential_id=00000000-0000-4000-8000-000000000000", 404},
		"credential of another device":    {"&credential_id=" + credB.ID, 404},
	} {
		if r := e.call("", "GET", base+c.query, nil); r.code != c.want {
			t.Errorf("%s → %d %s, want %d", name, r.code, r.body, c.want)
		}
	}
}
