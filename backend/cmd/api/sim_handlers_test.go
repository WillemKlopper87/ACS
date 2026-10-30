package main

import (
	"net/http/httptest"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/operators"
	"acs/internal/parameters"
)

func TestBuildSIMViewMasksAndWithholdsPIN(t *testing.T) {
	now := time.Now()
	names := map[string]bool{
		"Device.Cellular.Interface.1.USIM.IMSI":          false,
		"Device.Cellular.Interface.1.USIM.ICCID":         false,
		"Device.Cellular.Interface.1.USIM.MSISDN":        false,
		"Device.Cellular.Interface.1.USIM.PIN":           true,
		"Device.Cellular.Interface.1.USIM.Status":        false,
		"Device.Cellular.Interface.1.X_ALU-COM_MCC":      false,
		"Device.Cellular.Interface.1.X_ALU-COM_ESIM.Eid": false,
		"Device.Cellular.Interface.1.RSRP":               false,
	}
	cache := map[string]parameters.CachedValue{
		"Device.Cellular.Interface.1.USIM.IMSI":     {Value: "655010123456789", UpdatedAt: now},
		"Device.Cellular.Interface.1.USIM.ICCID":    {Value: "8927010000123456789", UpdatedAt: now},
		"Device.Cellular.Interface.1.USIM.PIN":      {Value: "1234", UpdatedAt: now},
		"Device.Cellular.Interface.1.USIM.Status":   {Value: "Valid", UpdatedAt: now},
		"Device.Cellular.Interface.1.X_ALU-COM_MCC": {Value: "655", UpdatedAt: now},
	}

	masked := buildSIMView("d1", names, cache, false)
	if !masked.Supported || masked.Revealed {
		t.Fatalf("supported=%v revealed=%v", masked.Supported, masked.Revealed)
	}
	if len(masked.Identifiers) != 2 {
		t.Fatalf("identifiers = %+v", masked.Identifiers)
	}
	for _, f := range masked.Identifiers {
		if !f.Masked || f.Value == cache[f.Path].Value {
			t.Errorf("%s not masked: %q", f.Path, f.Value)
		}
		if f.Path == "Device.Cellular.Interface.1.USIM.PIN" {
			t.Fatal("PIN listed")
		}
	}
	if len(masked.Context) != 2 {
		t.Fatalf("context = %+v", masked.Context)
	}
	wantRefresh := []string{"Device.Cellular.Interface.1.USIM.MSISDN", "Device.Cellular.Interface.1.X_ALU-COM_ESIM.Eid"}
	if len(masked.RefreshPaths) != 2 || masked.RefreshPaths[0] != wantRefresh[0] || masked.RefreshPaths[1] != wantRefresh[1] {
		t.Fatalf("refresh = %v", masked.RefreshPaths)
	}

	full := buildSIMView("d1", names, cache, true)
	for _, f := range full.Identifiers {
		if f.Masked || f.Value != cache[f.Path].Value {
			t.Errorf("%s masked in revealed view: %q", f.Path, f.Value)
		}
	}
}

func TestBuildSIMViewWithoutCellular(t *testing.T) {
	view := buildSIMView("beacon", map[string]bool{"Device.DeviceInfo.SerialNumber": false}, nil, false)
	if view.Supported || view.Reason == "" {
		t.Fatalf("view = %+v", view)
	}
}

func TestCanRevealSubscriberIdentity(t *testing.T) {
	cases := []struct {
		name   string
		claims *auth.Claims
		want   bool
	}{
		{"no claims (lab mode)", nil, false},
		{"manager", &auth.Claims{Subject: "alice", Role: operators.RoleManager}, false},
		{"bss service token", &auth.Claims{Subject: serviceSubject, Role: operators.RoleSuperAdmin}, false},
		{"superadmin", &auth.Claims{Subject: "willem", Role: operators.RoleSuperAdmin}, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/v1/devices/d1/sim/reveal", nil)
		if c.claims != nil {
			r = r.WithContext(withOperatorClaims(r.Context(), c.claims))
		}
		if got := canRevealSubscriberIdentity(r); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
