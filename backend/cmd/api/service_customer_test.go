package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"acs/internal/operators"
)

// The BSS adapter's service token used to reach every device. A call now
// names the customer it acts for and is confined to that customer's
// devices (docs/SECURITY-ASSESSMENT-2026-11-14.md, O5).
func TestIntegration_ServiceCallsAreConfinedToTheirCustomer(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("ISP A"), e.customer("ISP B")
	e.operator("root", operators.RoleSuperAdmin)
	devA, devB := e.device("SC0001A", &custA), e.device("SC0002B", &custB)

	service := func(method, path, acting string, body string) resp {
		t.Helper()
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
		req.Header.Set("Authorization", "Bearer "+testServiceToken)
		if acting != "" {
			req.Header.Set(actingCustomerHeader, acting)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return resp{r.StatusCode, string(b)}
	}
	write := `{"parameters":[{"name":"Device.WiFi.SSID.1.SSID","value":"x","type":"xsd:string"}]}`

	// Acting for A: A's device is reachable on every service route, B's
	// is not found on any of them, and nothing is queued for it.
	routes := []struct{ method, suffix, body string }{
		{"GET", "", ""},
		{"GET", "/parameter-names", ""},
		{"GET", "/wifi/networks", ""},
		{"GET", "/parameters?paths=Device.WiFi.SSID.1.SSID", ""},
		{"PUT", "/parameters", write},
	}
	var jobA string
	for _, rt := range routes {
		own := service(rt.method, "/api/v1/devices/"+devA+rt.suffix, custA, rt.body)
		if own.code/100 != 2 {
			t.Errorf("A's adapter, A's device: %s %s = %d %s", rt.method, rt.suffix, own.code, own.body)
		}
		if rt.method == "PUT" {
			if i := strings.Index(own.body, `"command_key":"`); i >= 0 {
				jobA = own.body[i+len(`"command_key":"`):]
				jobA = jobA[:strings.Index(jobA, `"`)]
			}
		}
		other := service(rt.method, "/api/v1/devices/"+devB+rt.suffix, custA, rt.body)
		if other.code != 404 {
			t.Errorf("A's adapter, B's device: %s %s = %d %s, want 404", rt.method, rt.suffix, other.code, other.body)
		}
	}
	var queuedForB int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM jobs WHERE device_id = $1`, devB).Scan(&queuedForB); err != nil || queuedForB != 0 {
		t.Fatalf("jobs queued for B's device by A's adapter = %d (%v)", queuedForB, err)
	}

	// A job is its device's: B's adapter cannot read A's job.
	if jobA == "" {
		t.Fatal("no command key from A's write")
	}
	if r := service("GET", "/api/v1/jobs/"+jobA, custA, ""); r.code != 200 {
		t.Fatalf("A's adapter reads its job = %d %s", r.code, r.body)
	}
	if r := service("GET", "/api/v1/jobs/"+jobA, custB, ""); r.code != 404 {
		t.Fatalf("B's adapter reads A's job = %d, want 404", r.code)
	}

	// The header is a customer ID or nothing.
	if r := service("GET", "/api/v1/devices/"+devA, "everyone", ""); r.code != 400 {
		t.Fatalf("malformed acting customer = %d, want 400", r.code)
	}
	// The adapter reads named parameters only, never a whole cache.
	if r := service("GET", "/api/v1/devices/"+devA+"/parameters", custA, ""); r.code != 403 {
		t.Fatalf("whole parameter cache with the service token = %d, want 403", r.code)
	}

	// Without the header a call still reaches the fleet (lab, fleet-wide
	// clients) unless the deployment requires one.
	if r := service("GET", "/api/v1/devices/"+devB, "", ""); r.code != 200 {
		t.Fatalf("no acting customer, not required = %d", r.code)
	}
	serviceCallsRequireCustomer = true
	t.Cleanup(func() { serviceCallsRequireCustomer = false })
	if r := service("GET", "/api/v1/devices/"+devB, "", ""); r.code != 403 || !strings.Contains(r.body, actingCustomerHeader) {
		t.Fatalf("no acting customer, required = %d %s, want 403", r.code, r.body)
	}
	if r := service("GET", "/api/v1/devices/"+devA, custA, ""); r.code != 200 {
		t.Fatalf("named customer, required = %d", r.code)
	}

	// An operator cannot borrow the mechanism: the header means nothing on
	// an operator's own session.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/devices/"+devB, nil)
	req.Header.Set("Authorization", "Bearer "+e.tokens["root"])
	req.Header.Set(actingCustomerHeader, custA)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("superadmin with the header = %d, want it ignored (200)", r.StatusCode)
	}
}
