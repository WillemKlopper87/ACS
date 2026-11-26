package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"acs/internal/operators"
	"acs/internal/tenancy"
)

func TestIntegration_SignalHistory(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("Signal A"), e.customer("Signal B")
	devA, devB := e.device("SG0001A", &custA), e.device("SG0002B", &custB)
	e.operator("alice", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	path := func(id, q string) string { return "/api/v1/devices/" + id + "/signal-history" + q }

	t.Run("empty history is an empty array", func(t *testing.T) {
		got := e.call("alice", "GET", path(devA, ""), nil)
		if got.code != 200 {
			t.Fatalf("code = %d %s", got.code, got.body)
		}
		if !strings.Contains(got.body, `"samples":[]`) {
			t.Fatalf("body = %s, want samples []", got.body)
		}
	})
	t.Run("bad hours are 400", func(t *testing.T) {
		for _, q := range []string{"?hours=0", "?hours=721", "?hours=abc", "?hours=1.5"} {
			if got := e.call("alice", "GET", path(devA, q), nil); got.code != 400 {
				t.Errorf("%s = %d, want 400", q, got.code)
			}
		}
		if got := e.call("alice", "GET", path(devA, "?hours=720"), nil); got.code != 200 {
			t.Errorf("hours=720 = %d, want 200", got.code)
		}
	})
	t.Run("another tenant's device is 404", func(t *testing.T) {
		if got := e.call("alice", "GET", path(devB, ""), nil); got.code != 404 {
			t.Fatalf("code = %d, want 404", got.code)
		}
	})
	t.Run("stored samples come back with null metrics preserved", func(t *testing.T) {
		now := time.Now().UTC()
		for i, rsrp := range []any{-95.5, nil, -101.0} {
			if _, err := e.db.ExecContext(e.ctx,
				`INSERT INTO device_cpe_samples (device_id, sampled_at, rsrp, band, rat) VALUES ($1,$2,$3,'B3','LTE')`,
				devA, now.Add(-time.Duration(3-i)*time.Hour), rsrp); err != nil {
				t.Fatal(err)
			}
		}
		got := e.call("alice", "GET", path(devA, "?hours=24"), nil)
		if got.code != 200 {
			t.Fatalf("code = %d %s", got.code, got.body)
		}
		var h struct {
			BucketSeconds int64 `json:"bucket_seconds"`
			Samples       []struct {
				RSRP *float64 `json:"rsrp"`
				RSRQ *float64 `json:"rsrq"`
				Band string   `json:"band"`
			} `json:"samples"`
		}
		if err := json.Unmarshal([]byte(got.body), &h); err != nil {
			t.Fatal(err)
		}
		if len(h.Samples) != 3 || h.BucketSeconds != 0 {
			t.Fatalf("samples = %d bucket = %d, want 3 and 0", len(h.Samples), h.BucketSeconds)
		}
		if h.Samples[0].RSRP == nil || *h.Samples[0].RSRP != -95.5 || h.Samples[1].RSRP != nil || h.Samples[0].RSRQ != nil || h.Samples[0].Band != "B3" {
			t.Fatalf("samples = %+v", h.Samples)
		}
		if !strings.Contains(got.body, `"rsrp":null`) {
			t.Fatalf("body = %s, want an explicit null", got.body)
		}
	})
}
