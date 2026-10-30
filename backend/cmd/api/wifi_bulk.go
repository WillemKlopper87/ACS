package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/jobs"
	"acs/internal/parameters"
)

// bulkActionSetWiFi is the fleet "set the Wi-Fi" action. It replaces the
// console's old SET_PARAMETER of a fixed Device.WiFi.SSID.1.SSID, which on
// a multi-SSID EasyMesh controller (Nokia FastMile: SSID.1 2.4 GHz and
// SSID.5 5 GHz primary, guest and disabled factory SSIDs beside them)
// renamed one band only and split the network.
const bulkActionSetWiFi = "SET_WIFI"

// wifiSettings is what SET_WIFI writes; every field is optional but at
// least one must be set.
type wifiSettings struct {
	SSID     string
	Password string
	Enabled  *bool
}

func (s wifiSettings) validate() error {
	if s.SSID == "" && s.Password == "" && s.Enabled == nil {
		return errors.New("SET_WIFI requires at least one of wifi_ssid, wifi_password, wifi_enabled")
	}
	if s.SSID != "" && (strings.TrimSpace(s.SSID) != s.SSID || len(s.SSID) > 32) {
		return errors.New("wifi_ssid must be 1-32 bytes without leading/trailing spaces")
	}
	if s.Password != "" && (len(s.Password) < 8 || len(s.Password) > 63) {
		return errors.New("wifi_password must be an 8-63 character WPA passphrase")
	}
	return nil
}

// errWiFiRolesUnknown means the resolver's view is incomplete: the values
// that decide which SSIDs are primary are discovered but not cached, so no
// write is planned; the caller queues a read of the plan's RefreshPaths.
var errWiFiRolesUnknown = errors.New("Wi-Fi roles not known yet")

// wifiWritePlan is one device's SET_WIFI outcome before anything is queued.
type wifiWritePlan struct {
	Payload jobs.SetParameterPayload
	// Detail names the networks written, e.g. "SSID 1 (2.4GHz) + SSID 5 (5GHz)".
	Detail string
	// RefreshPaths is set with errWiFiRolesUnknown: the non-secret values
	// to read before the device's primary networks can be decided.
	RefreshPaths []string
}

// planWiFiWrites decides what SET_WIFI writes on one device, from its
// data-model root, discovered names (path → writable; nil = never
// discovered) and cached values. TR-181 writes every primary network the
// resolver returns (guest, unbound and disabled SSIDs are never touched);
// TR-098 keeps the canonical WLANConfiguration.1 paths, gated on discovery
// like BSS MODIFY_WIFI.
func planWiFiWrites(root string, names map[string]bool, cache map[string]parameters.CachedValue, in wifiSettings) (wifiWritePlan, error) {
	var writes []parameterInput
	var detail string
	enabled := func() string {
		if *in.Enabled {
			return "1"
		}
		return "0"
	}

	if root == devices.DataModelRootIGD1 {
		add := func(p adapters.CanonicalParameter, value, typ string) error {
			path, err := adapters.ResolveWritablePath(root, p, names)
			if err != nil {
				return err
			}
			writes = append(writes, parameterInput{Name: path, Value: value, Type: typ})
			return nil
		}
		if in.SSID != "" {
			if err := add(adapters.WiFiSSID, in.SSID, "xsd:string"); err != nil {
				return wifiWritePlan{}, fmt.Errorf("Wi-Fi SSID: %w", err)
			}
		}
		if in.Password != "" {
			if err := add(adapters.WiFiKeyPassphrase, in.Password, "xsd:string"); err != nil {
				return wifiWritePlan{}, fmt.Errorf("Wi-Fi passphrase: %w", err)
			}
		}
		if in.Enabled != nil {
			if err := add(adapters.WiFiEnable, enabled(), "xsd:boolean"); err != nil {
				return wifiWritePlan{}, fmt.Errorf("Wi-Fi enable: %w", err)
			}
		}
		detail = "TR-098 WLANConfiguration.1"
	} else {
		if len(names) == 0 {
			return wifiWritePlan{}, errors.New("run parameter discovery first: Wi-Fi networks are resolved from the discovered tree")
		}
		view := adapters.ResolveWiFiNetworks(names, cache)
		if len(view.Networks) == 0 {
			return wifiWritePlan{}, errors.New("device has no Device.WiFi.SSID networks (Wi-Fi may be managed by its gateway)")
		}
		if !view.Complete {
			return wifiWritePlan{RefreshPaths: view.RefreshPaths}, errWiFiRolesUnknown
		}
		primary := adapters.PrimaryWiFiNetworks(view)
		if len(primary) == 0 {
			return wifiWritePlan{}, errors.New("device has no primary (enabled, non-guest) Wi-Fi network")
		}
		labels := make([]string, 0, len(primary))
		for _, n := range primary {
			if in.SSID != "" {
				if n.NamePath == "" || !n.NameWritable {
					return wifiWritePlan{}, fmt.Errorf("SSID name of network %d is not writable", n.Instance)
				}
				writes = append(writes, parameterInput{Name: n.NamePath, Value: in.SSID, Type: "xsd:string"})
			}
			if in.Password != "" {
				if n.PassphrasePath == "" || !n.PassphraseWrite {
					return wifiWritePlan{}, fmt.Errorf("passphrase of network %d is not writable", n.Instance)
				}
				writes = append(writes, parameterInput{Name: n.PassphrasePath, Value: in.Password, Type: "xsd:string"})
			}
			if in.Enabled != nil {
				// Note: disabling turns these networks into role "disabled",
				// so a later SET_WIFI cannot re-enable them by role; that
				// needs a per-network write from the device page.
				if n.EnablePath == "" || !n.EnableWritable {
					return wifiWritePlan{}, fmt.Errorf("enable flag of network %d is not writable", n.Instance)
				}
				writes = append(writes, parameterInput{Name: n.EnablePath, Value: enabled(), Type: "xsd:boolean"})
			}
			label := "SSID " + strconv.Itoa(n.Instance)
			if n.Band != "" {
				label += " (" + n.Band + ")"
			}
			labels = append(labels, label)
		}
		detail = strings.Join(labels, " + ")
	}

	payload, err := buildSetParameterPayload(writes)
	if err != nil {
		return wifiWritePlan{}, err
	}
	return wifiWritePlan{Payload: payload, Detail: detail}, nil
}

// bulkSetWiFi plans and queues SET_WIFI for one device: one verified
// SET_PARAMETER job carrying every primary write, or — when the device's
// Wi-Fi roles are not cached yet — the targeted read that completes them
// (reported as an error so the operator retries after the next session).
func (h *handler) bulkSetWiFi(ctx context.Context, deviceID string, in wifiSettings, operator string) bulkActionResult {
	result := bulkActionResult{DeviceID: deviceID}
	device, err := h.devices.Get(ctx, deviceID)
	if err != nil {
		result.Error = "device not found"
		return result
	}
	var names map[string]bool
	discovered, err := h.params.GetNames(ctx, deviceID)
	if err != nil {
		h.logger.Error("failed to read discovered parameter names", "err", err, "device_id", deviceID)
		result.Error = "internal error"
		return result
	}
	if discovered != nil {
		names = discovered.Names
	}
	cache, err := h.params.Get(ctx, deviceID)
	if err != nil {
		h.logger.Error("failed to read parameter cache", "err", err, "device_id", deviceID)
		result.Error = "internal error"
		return result
	}

	plan, err := planWiFiWrites(device.DataModelRoot, names, cache, in)
	if errors.Is(err, errWiFiRolesUnknown) {
		job, qerr := h.jobs.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: plan.RefreshPaths}, operator)
		if qerr != nil {
			result.Error = err.Error() + "; queuing the read failed: " + qerr.Error()
			return result
		}
		result.RefreshCommandKey = job.CommandKey
		result.Error = err.Error() + ": queued a read of them, retry after the device's next session"
		return result
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	job, err := h.jobs.Create(ctx, deviceID, jobs.TypeSetParameter, plan.Payload, operator)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.CommandKey = job.CommandKey
	result.Detail = plan.Detail
	return result
}
