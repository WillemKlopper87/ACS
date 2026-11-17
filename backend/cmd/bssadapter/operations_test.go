package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"acs/internal/bss"
)

type acsCall struct{ method, path, key string }

// operationsACS is an operator API that records the operations it is asked
// for and answers 202 with a command key, or the status it is told to.
type operationsACS struct {
	mu     sync.Mutex
	calls  []acsCall
	status int
}

func (a *operationsACS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet { // a job status read, for a resent order
		json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-operation", "status": "QUEUED"})
		return
	}
	a.mu.Lock()
	a.calls = append(a.calls, acsCall{r.Method, r.URL.Path, r.Header.Get("Idempotency-Key")})
	status := a.status
	a.mu.Unlock()
	if status == 0 {
		status = http.StatusAccepted
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-operation"})
}

func postOrder(h *handler, req createOrderRequest) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	h.createOrder(rec, httptest.NewRequest(http.MethodPost, "/bss/v1/orders", bytes.NewReader(body)))
	return rec
}

func TestDeviceOperationOrdersTravelTheOutbox(t *testing.T) {
	acs := &operationsACS{}
	ctx, h, db := newOrderTestHandler(t, acs)
	const accountID, deviceID = "acct-op", "22222222-2222-2222-2222-222222222222"
	seedOrderDevice(t, ctx, db, accountID, deviceID)

	for action, path := range map[string]string{
		bss.ActionReboot:        "/api/v1/devices/" + deviceID + "/reboot",
		bss.ActionRefreshStatus: "/api/v1/devices/" + deviceID + "/parameters/refresh-status",
	} {
		acs.calls = nil
		orderID := "ord-" + action
		rec := postOrder(h, createOrderRequest{ExternalOrderID: orderID, AccountID: accountID, Action: action})
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status = %d, body: %s", action, rec.Code, rec.Body.String())
		}
		if len(acs.calls) != 1 || acs.calls[0] != (acsCall{http.MethodPost, path, "bss:" + orderID + ":operation"}) {
			t.Fatalf("%s: calls = %+v", action, acs.calls)
		}
		order, err := h.mappings.FindOrder(ctx, orderID)
		if err != nil || order == nil {
			t.Fatalf("%s: FindOrder: %v", action, err)
		}
		if order.Status != bss.OrderStatusDispatched || order.CommandKey != "ck-operation" || order.Action != action || len(order.Parameters) != 0 {
			t.Errorf("%s: order = %+v", action, order)
		}
		// The same order again is a retry: answered from the outbox, the
		// router is not restarted a second time.
		again := postOrder(h, createOrderRequest{ExternalOrderID: orderID, AccountID: accountID, Action: action})
		if again.Code != http.StatusAccepted || len(acs.calls) != 1 {
			t.Errorf("%s: resend status = %d, calls = %d", action, again.Code, len(acs.calls))
		}
	}

	// The same order ID for a different operation is not a retry.
	changed := postOrder(h, createOrderRequest{ExternalOrderID: "ord-" + bss.ActionReboot, AccountID: accountID, Action: bss.ActionRefreshStatus})
	if changed.Code != http.StatusConflict {
		t.Errorf("changed resend: status = %d, want 409; body: %s", changed.Code, changed.Body.String())
	}
}

func TestDeviceOperationOrdersAreCheckedBeforeAnythingIsWritten(t *testing.T) {
	acs := &operationsACS{}
	ctx, h, db := newOrderTestHandler(t, acs)
	const accountID, deviceID = "acct-op", "33333333-3333-3333-3333-333333333333"
	seedOrderDevice(t, ctx, db, accountID, deviceID)
	confirm := map[string]string{"confirm": "FACTORY_RESET"}

	refused := []struct {
		name string
		req  createOrderRequest
		code string
	}{
		{"reboot with a parameter", createOrderRequest{ExternalOrderID: "r1", AccountID: accountID, Action: bss.ActionReboot, Parameters: map[string]string{"x": "1"}}, "ErrInvalidRequest"},
		{"factory reset while not enabled", createOrderRequest{ExternalOrderID: "r2", AccountID: accountID, Action: bss.ActionFactoryReset, Parameters: confirm}, "ErrUnsupportedAction"},
	}
	for _, c := range refused {
		rec := postOrder(h, c.req)
		var problem struct {
			Error string `json:"error"`
		}
		json.Unmarshal(rec.Body.Bytes(), &problem)
		if rec.Code != http.StatusBadRequest || problem.Error != c.code {
			t.Errorf("%s: status = %d, error = %q; body: %s", c.name, rec.Code, problem.Error, rec.Body.String())
		}
		if order, _ := h.mappings.FindOrder(ctx, c.req.ExternalOrderID); order != nil {
			t.Errorf("%s: an order row was written", c.name)
		}
	}
	if len(acs.calls) != 0 {
		t.Fatalf("a refused order reached the ACS: %+v", acs.calls)
	}

	h.allowFactoryReset = true
	unconfirmed := postOrder(h, createOrderRequest{ExternalOrderID: "r3", AccountID: accountID, Action: bss.ActionFactoryReset})
	if unconfirmed.Code != http.StatusBadRequest || len(acs.calls) != 0 {
		t.Errorf("unconfirmed factory reset: status = %d, calls = %d", unconfirmed.Code, len(acs.calls))
	}
	ok := postOrder(h, createOrderRequest{ExternalOrderID: "r4", AccountID: accountID, Action: bss.ActionFactoryReset, Parameters: confirm})
	if ok.Code != http.StatusAccepted || len(acs.calls) != 1 || acs.calls[0].path != "/api/v1/devices/"+deviceID+"/factory-reset" {
		t.Errorf("confirmed factory reset: status = %d, calls = %+v; body: %s", ok.Code, acs.calls, ok.Body.String())
	}
}

// A dispatch whose answer was lost is retried by the reconciler with the
// order's own action and the SAME idempotency key, so the operator API
// answers with the job it already queued.
func TestReconcilerRetriesADeviceOperationAsAnOperation(t *testing.T) {
	acs := &operationsACS{status: http.StatusBadGateway}
	ctx, h, db := newOrderTestHandler(t, acs)
	const accountID, deviceID = "acct-op", "44444444-4444-4444-4444-444444444444"
	seedOrderDevice(t, ctx, db, accountID, deviceID)

	first := postOrder(h, createOrderRequest{ExternalOrderID: "ord-retry", AccountID: accountID, Action: bss.ActionReboot})
	if first.Code == http.StatusAccepted {
		t.Fatalf("the first dispatch should have failed; body: %s", first.Body.String())
	}
	acs.mu.Lock()
	acs.status = http.StatusAccepted
	acs.mu.Unlock()
	backdateOrderLastAttempt(t, ctx, db, "ord-retry")
	h.reconcilePendingOrders(ctx)

	order, err := h.mappings.FindOrder(ctx, "ord-retry")
	if err != nil || order == nil || order.Status != bss.OrderStatusDispatched || order.CommandKey != "ck-operation" {
		t.Fatalf("order after retry = %+v, err = %v", order, err)
	}
	want := acsCall{http.MethodPost, "/api/v1/devices/" + deviceID + "/reboot", "bss:ord-retry:operation"}
	if len(acs.calls) != 2 || acs.calls[0] != want || acs.calls[1] != want {
		t.Errorf("calls = %+v, want the same reboot call twice", acs.calls)
	}
}

func TestPreflightOfADeviceOperationWritesNothing(t *testing.T) {
	acs := &operationsACS{}
	ctx, h, db := newOrderTestHandler(t, acs)
	const accountID, deviceID = "acct-op", "55555555-5555-5555-5555-555555555555"
	seedOrderDevice(t, ctx, db, accountID, deviceID)

	preflight := func(action string, params map[string]string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(actionPreflightRequest{AccountID: accountID, Action: action, Parameters: params})
		rec := httptest.NewRecorder()
		h.preflightAction(rec, httptest.NewRequest(http.MethodPost, "/bss/v1/actions/preflight", bytes.NewReader(body)))
		return rec
	}
	ok := preflight(bss.ActionReboot, nil)
	var out actionPreflightResponse
	if err := json.Unmarshal(ok.Body.Bytes(), &out); err != nil || ok.Code != http.StatusOK || out.DeviceID != deviceID || len(out.Parameters) != 0 {
		t.Errorf("reboot preflight: status = %d, out = %+v, err = %v", ok.Code, out, err)
	}
	if rec := preflight(bss.ActionFactoryReset, map[string]string{"confirm": "FACTORY_RESET"}); rec.Code != http.StatusBadRequest {
		t.Errorf("factory reset preflight while not enabled: status = %d", rec.Code)
	}
	if len(acs.calls) != 0 {
		t.Errorf("a preflight reached the ACS: %+v", acs.calls)
	}
}

// The status read asks the operator API only for the status parameters the
// device has, and returns values, never paths.
func TestMappedDeviceStatusReadsTheCacheOnly(t *testing.T) {
	const accountID, deviceID = "acct-op", "66666666-6666-6666-6666-666666666666"
	var asked []string
	acs := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("a status read must not write: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v1/devices/" + deviceID:
			json.NewEncoder(w).Encode(map[string]string{"id": deviceID, "data_model_root": "DEVICE2"})
		case "/api/v1/devices/" + deviceID + "/parameter-names":
			json.NewEncoder(w).Encode(map[string]any{"discovered_at": "2026-11-17T07:00:00Z", "names": map[string]bool{
				"Device.DeviceInfo.UpTime": false, "Device.Hosts.HostNumberOfEntries": false,
				"Device.Cellular.Interface.1.RSRP": false, "Device.WiFi.SSID.1.SSID": true,
			}})
		case "/api/v1/devices/" + deviceID + "/parameters":
			asked = append(asked, r.URL.Query().Get("paths"))
			json.NewEncoder(w).Encode(map[string]any{"parameters": map[string]bss.CachedParameter{
				"Device.DeviceInfo.UpTime":         {Value: "3600", UpdatedAt: "2026-11-17T08:00:00Z"},
				"Device.Cellular.Interface.1.RSRP": {Value: "-101", UpdatedAt: "2026-11-17T08:01:00Z"},
			}})
		default:
			http.NotFound(w, r)
		}
	})
	ctx, h, db := newOrderTestHandler(t, acs)
	seedOrderDevice(t, ctx, db, accountID, deviceID)

	read := func(account string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/bss/v1/mappings/"+account+"/status", nil)
		req.SetPathValue("account_id", account)
		rec := httptest.NewRecorder()
		h.getMappedDeviceStatus(rec, req)
		return rec
	}
	rec := read(accountID)
	want := `{"account_id":"acct-op","device_id":"` + deviceID + `","role":"gateway","uptime_seconds":3600,"lan_hosts":null,"signal":{"rsrp":-101,"rsrq":null,"sinr":null,"rssi":null},"read_at":"2026-11-17T08:00:00Z"}`
	if rec.Code != http.StatusOK || bytes.TrimSpace(rec.Body.Bytes()) == nil || string(bytes.TrimSpace(rec.Body.Bytes())) != want {
		t.Errorf("status = %d, body = %s\nwant %s", rec.Code, rec.Body.String(), want)
	}
	if len(asked) != 1 || asked[0] != "Device.DeviceInfo.UpTime,Device.Hosts.HostNumberOfEntries,Device.Cellular.Interface.1.RSRP" {
		t.Errorf("paths asked = %v", asked)
	}
	if rec := read("acct-unknown"); rec.Code != http.StatusNotFound {
		t.Errorf("an account with no device: status = %d, want 404", rec.Code)
	}
}
