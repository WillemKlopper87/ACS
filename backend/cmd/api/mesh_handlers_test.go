package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/operators"
	"acs/internal/tenancy"
)

func TestToResponseCarriesGatewayLink(t *testing.T) {
	gw, key := "11111111-1111-4111-8111-111111111111", "A4FC77+3TG02241+CSC25430018F"
	body, err := json.Marshal(toResponse(devices.Device{ID: "sat", GatewayDeviceID: &gw, GatewayOUISerial: &key}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"gateway_device_id":"` + gw + `"`, `"gateway_oui_serial":"` + key + `"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("response %s missing %s", body, want)
		}
	}
	plain, _ := json.Marshal(toResponse(devices.Device{ID: "gw"}))
	if strings.Contains(string(plain), "gateway_") {
		t.Errorf("unlinked device response should omit gateway fields: %s", plain)
	}
}

func TestDeviceSatellitesEndpoint(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("Customer A"), e.customer("Customer B")
	gatewayID := e.device("GW001", &custA)
	satA := e.device("SAT001", &custA)
	satB := e.device("SAT002", &custB)
	e.operator("alice", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.operator("root", operators.RoleSuperAdmin)

	gateway := cwmp.DeviceID{OUI: "ABCDEF", ProductClass: "CPE", SerialNumber: "GW001"}
	for _, id := range []string{satA, satB} {
		if _, err := e.h.devices.LinkGateway(e.ctx, id, gateway, true); err != nil {
			t.Fatalf("link %s: %v", id, err)
		}
	}

	r := e.call("alice", http.MethodGet, "/api/v1/devices/"+gatewayID+"/satellites", nil)
	if r.code != http.StatusOK {
		t.Fatalf("satellites -> %d %s", r.code, r.body)
	}
	if !strings.Contains(r.body, satA) || strings.Contains(r.body, satB) {
		t.Errorf("alice satellites should include %s only: %s", satA, r.body)
	}
	if r = e.call("root", http.MethodGet, "/api/v1/devices/"+gatewayID+"/satellites", nil); !strings.Contains(r.body, satB) {
		t.Errorf("superadmin satellites missing %s: %s", satB, r.body)
	}
	if r = e.call("alice", http.MethodGet, "/api/v1/devices/"+satA, nil); !strings.Contains(r.body, `"gateway_device_id":"`+gatewayID+`"`) {
		t.Errorf("satellite device missing gateway_device_id: %s", r.body)
	}

	tmf := e.call("alice", http.MethodGet, "/tmf-api/resourceInventoryManagement/v5/resource/"+gatewayID, nil)
	if tmf.code != http.StatusOK || !strings.Contains(tmf.body, "hasMeshSatellite") || !strings.Contains(tmf.body, satA) || strings.Contains(tmf.body, satB) {
		t.Errorf("TMF639 gateway relationships should name in-scope %s only: %d %s", satA, tmf.code, tmf.body)
	}
	tmf = e.call("alice", http.MethodGet, "/tmf-api/resourceInventoryManagement/v5/resource/"+satA, nil)
	if !strings.Contains(tmf.body, "isMeshSatelliteOf") || !strings.Contains(tmf.body, gatewayID) {
		t.Errorf("TMF639 satellite missing gateway relationship: %s", tmf.body)
	}
}
