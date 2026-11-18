package bss

import (
	"sort"
	"strconv"
	"strings"
)

// DeviceStatus is what a support desk asks of a customer's router, read
// from the ACS's parameter cache (bss-contract 2.1.0, GET
// /bss/v1/mappings/{account_id}/status). Every value is as old as the
// CPE's last report of it: ReadAt is the OLDEST of the values given, so a
// caller can tell a fresh reading from a stale one, and REFRESH_STATUS
// (an order) asks the CPE to report again. A value the CPE has not
// reported is null, never zero.
type DeviceStatus struct {
	AccountID     string        `json:"account_id"`
	DeviceID      string        `json:"device_id"`
	Role          string        `json:"role"`
	UptimeSeconds *int64        `json:"uptime_seconds"`
	LANHosts      *int64        `json:"lan_hosts"`
	Signal        *DeviceSignal `json:"signal"`
	ReadAt        *string       `json:"read_at"`
}

// DeviceSignal is a cellular router's radio reading, in the units the CPE
// reports (dBm for RSRP and RSSI, dB for RSRQ and SINR). Null as a whole
// for a router with no cellular interface.
type DeviceSignal struct {
	RSRP *float64 `json:"rsrp"`
	RSRQ *float64 `json:"rsrq"`
	SINR *float64 `json:"sinr"`
	RSSI *float64 `json:"rssi"`
}

const maxStatusPaths = 40

var signalLeaves = []string{"RSRP", "RSRQ", "SINR", "RSSI"}

// StatusPaths picks the parameters a status read needs from what the
// device is known to have: its uptime and LAN host count, and one path
// per signal metric under a cellular subtree. With no discovery (names
// nil) only the uptime of the data model is asked for.
func StatusPaths(dataModelRoot string, names map[string]bool) []string {
	uptime, hosts := "Device.DeviceInfo.UpTime", "Device.Hosts.HostNumberOfEntries"
	if dataModelRoot == "IGD1" {
		uptime, hosts = "InternetGatewayDevice.DeviceInfo.UpTime", "InternetGatewayDevice.LANDevice.1.Hosts.HostNumberOfEntries"
	}
	if names == nil {
		return []string{uptime}
	}
	var paths []string
	for _, path := range []string{uptime, hosts} {
		if _, ok := names[path]; ok {
			paths = append(paths, path)
		}
	}
	for _, path := range signalPaths(names) {
		if len(paths) >= maxStatusPaths {
			break
		}
		paths = append(paths, path)
	}
	return paths
}

// signalPaths returns, per metric, the first cellular path (sorted, so the
// choice is stable) whose leaf is exactly the metric's name.
func signalPaths(names map[string]bool) []string {
	sorted := make([]string, 0, len(names))
	for path := range names {
		if strings.Contains(strings.ToUpper(path), "CELLULAR") && !strings.HasSuffix(path, ".") {
			sorted = append(sorted, path)
		}
	}
	sort.Strings(sorted)
	var out []string
	for _, leaf := range signalLeaves {
		for _, path := range sorted {
			if strings.EqualFold(path[strings.LastIndex(path, ".")+1:], leaf) {
				out = append(out, path)
				break
			}
		}
	}
	return out
}

// BuildDeviceStatus turns cached parameter values into a DeviceStatus. A
// value that is missing or not a number stays null.
func BuildDeviceStatus(accountID, deviceID, role string, values map[string]CachedParameter) DeviceStatus {
	status := DeviceStatus{AccountID: accountID, DeviceID: deviceID, Role: role}
	var signal DeviceSignal
	var oldest string
	seen := func(p CachedParameter) {
		if p.UpdatedAt != "" && (oldest == "" || p.UpdatedAt < oldest) {
			oldest = p.UpdatedAt
		}
	}
	for path, p := range values {
		leaf := strings.ToUpper(path[strings.LastIndex(path, ".")+1:])
		value := strings.TrimSpace(p.Value)
		switch leaf {
		case "UPTIME", "HOSTNUMBEROFENTRIES":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				continue
			}
			if leaf == "UPTIME" {
				status.UptimeSeconds = &n
			} else {
				status.LANHosts = &n
			}
			seen(p)
		case "RSRP", "RSRQ", "SINR", "RSSI":
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				continue
			}
			switch leaf {
			case "RSRP":
				signal.RSRP = &f
			case "RSRQ":
				signal.RSRQ = &f
			case "SINR":
				signal.SINR = &f
			default:
				signal.RSSI = &f
			}
			status.Signal = &signal
			seen(p)
		}
	}
	if oldest != "" {
		status.ReadAt = &oldest
	}
	return status
}
