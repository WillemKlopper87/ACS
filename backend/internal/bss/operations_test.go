package bss

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateDeviceOperation(t *testing.T) {
	confirm := map[string]string{"confirm": "FACTORY_RESET"}
	cases := []struct {
		name   string
		action string
		params map[string]string
		allow  bool
		want   error
	}{
		{"reboot", ActionReboot, nil, false, nil},
		{"refresh", ActionRefreshStatus, map[string]string{}, false, nil},
		{"reboot with a parameter", ActionReboot, map[string]string{"delay": "5"}, false, ErrInvalidParameters},
		{"not an operation", "MODIFY_WIFI", nil, true, ErrUnsupportedAction},
		{"factory reset, not enabled", ActionFactoryReset, confirm, false, ErrUnsupportedAction},
		{"factory reset, no confirmation", ActionFactoryReset, nil, true, ErrInvalidParameters},
		{"factory reset, wrong confirmation", ActionFactoryReset, map[string]string{"confirm": "yes"}, true, ErrInvalidParameters},
		{"factory reset, something extra", ActionFactoryReset, map[string]string{"confirm": "FACTORY_RESET", "x": "1"}, true, ErrInvalidParameters},
		{"factory reset", ActionFactoryReset, confirm, true, nil},
	}
	for _, c := range cases {
		err := ValidateDeviceOperation(c.action, c.params, c.allow)
		if !errors.Is(err, c.want) || (c.want == nil && err != nil) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "yes") {
			t.Errorf("%s: the error repeats a value the caller sent: %v", c.name, err)
		}
	}
}

func TestDeviceOperationCallsItsRouteWithTheIdempotencyKey(t *testing.T) {
	type call struct{ method, path, key string }
	var calls []call
	status := http.StatusAccepted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, call{r.Method, r.URL.Path, r.Header.Get("Idempotency-Key")})
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-op"})
	}))
	defer srv.Close()
	client := NewACSClient(srv.URL, 0, "")
	ctx := context.Background()

	for action, path := range map[string]string{
		ActionReboot:        "/api/v1/devices/dev-1/reboot",
		ActionFactoryReset:  "/api/v1/devices/dev-1/factory-reset",
		ActionRefreshStatus: "/api/v1/devices/dev-1/parameters/refresh-status",
	} {
		calls = nil
		key, err := client.DeviceOperation(ctx, "dev-1", action, "bss:ord-1:operation")
		if err != nil || key != "ck-op" {
			t.Fatalf("%s: key = %q, err = %v", action, key, err)
		}
		if len(calls) != 1 || calls[0] != (call{http.MethodPost, path, "bss:ord-1:operation"}) {
			t.Errorf("%s: calls = %+v", action, calls)
		}
	}

	calls = nil
	if _, err := client.DeviceOperation(ctx, "dev-1", "MODIFY_WIFI", "k"); !errors.Is(err, ErrUnsupportedAction) || len(calls) != 0 {
		t.Errorf("a parameter action must not reach the ACS: err = %v, calls = %+v", err, calls)
	}
	status = http.StatusConflict // no discovered status parameter
	if _, err := client.DeviceOperation(ctx, "dev-1", ActionRefreshStatus, "k"); !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("409: err = %v, want ErrUnsupportedAction", err)
	}
	status = http.StatusNotFound
	if _, err := client.DeviceOperation(ctx, "dev-1", ActionReboot, "k"); !errors.Is(err, ErrDeviceLookupNotFound) {
		t.Errorf("404: err = %v, want ErrDeviceLookupNotFound", err)
	}
	srv.Close()
	if _, err := client.DeviceOperation(ctx, "dev-1", ActionReboot, "k"); !errors.Is(err, ErrACSUnreachable) {
		t.Errorf("down: err = %v, want ErrACSUnreachable", err)
	}
}
