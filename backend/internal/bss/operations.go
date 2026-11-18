package bss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Device operations are the BSS actions that are not parameter writes: they
// ask the CPE to do something (bss-contract 2.1.0). They travel the same
// order pipeline as MODIFY_WIFI (POST /bss/v1/orders, the write-ahead
// outbox, GET /bss/v1/jobs/{command_key}) with no parameters, so an
// integration tracks them exactly like any other order.
const (
	// ActionReboot restarts the CPE. Its job ends when the CPE takes the
	// command; the CPE is back when its next Inform arrives.
	ActionReboot = "REBOOT"
	// ActionFactoryReset wipes the CPE's configuration. Destructive: off
	// unless the operator allows it (ACS_BSS_ALLOW_FACTORY_RESET), and the
	// order must say `confirm: FACTORY_RESET`.
	ActionFactoryReset = "FACTORY_RESET"
	// ActionRefreshStatus asks the CPE to report its status parameters now
	// (uptime, LAN host count, cellular signal where it has one). It
	// changes nothing on the CPE.
	ActionRefreshStatus = "REFRESH_STATUS"
)

var deviceOperationPaths = map[string]string{
	ActionReboot:        "reboot",
	ActionFactoryReset:  "factory-reset",
	ActionRefreshStatus: "parameters/refresh-status",
}

// IsDeviceOperation reports whether action is a device operation rather
// than a parameter-write action of the registry in template.go.
func IsDeviceOperation(action string) bool {
	_, ok := deviceOperationPaths[action]
	return ok
}

// ValidateDeviceOperation checks what an order may carry for a device
// operation: nothing, except the confirmation a factory reset needs. The
// error never repeats a value the caller sent.
func ValidateDeviceOperation(action string, params map[string]string, allowFactoryReset bool) error {
	if !IsDeviceOperation(action) {
		return fmt.Errorf("%w: %q", ErrUnsupportedAction, action)
	}
	if action != ActionFactoryReset {
		if len(params) != 0 {
			return fmt.Errorf("%w: %s takes no parameters", ErrInvalidParameters, action)
		}
		return nil
	}
	if !allowFactoryReset {
		return ErrFactoryResetNotEnabled
	}
	if len(params) != 1 || params["confirm"] != ActionFactoryReset {
		return fmt.Errorf("%w: FACTORY_RESET needs exactly one parameter, confirm=FACTORY_RESET", ErrInvalidParameters)
	}
	return nil
}

// ErrFactoryResetNotEnabled is the refusal of a FACTORY_RESET where the
// operator has not switched it on. It is an ErrUnsupportedAction.
var ErrFactoryResetNotEnabled = fmt.Errorf("%w: FACTORY_RESET is not enabled on this ACS (ACS_BSS_ALLOW_FACTORY_RESET)", ErrUnsupportedAction)

// DeviceOperation queues a device operation on the operator API and
// returns the job's command_key. The idempotency key makes a retried
// dispatch (the order outbox's) answer with the job already queued.
func (c *ACSClient) DeviceOperation(ctx context.Context, deviceID, action, idempotencyKey string) (commandKey string, err error) {
	path, ok := deviceOperationPaths[action]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedAction, action)
	}
	url := fmt.Sprintf("%s/api/v1/devices/%s/%s", c.baseURL, deviceID, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", fmt.Errorf("build device operation request: %w", err)
	}
	if strings.TrimSpace(idempotencyKey) != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrACSUnreachable, err)
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	switch status {
	case http.StatusAccepted:
		var out queueResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", fmt.Errorf("decode ACS response: %w", err)
		}
		return out.CommandKey, nil
	case http.StatusNotFound:
		return "", ErrDeviceLookupNotFound
	case http.StatusConflict:
		// refresh-status on a device with no discovered status parameter.
		return "", fmt.Errorf("%w: %s: the device has no parameter to answer it yet", ErrUnsupportedAction, action)
	default:
		return "", fmt.Errorf("unexpected status from ACS: %d", status)
	}
}
