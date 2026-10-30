// Package wifiinsights builds the controller-style Wi-Fi overview
// (bandwidth, top clients/SSIDs, client counts, new/returning clients,
// connection time, client vendors) that cloud controllers such as
// Grandstream GDMS show. CPEs keep no history and report only while a CWMP
// session is open, so the ACS samples cumulative counters on periodic
// Informs and derives every rate from the difference between samples
// (docs/WIFI-MANAGEMENT-SPEC.md §3.3a).
package wifiinsights

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"acs/internal/devices/adapters"
	"acs/internal/parameters"
)

// CreatedBy marks the sampling GET jobs, so the CWMP gateway records their
// response as a sample instead of writing it to the parameter cache (whose
// history would otherwise grow with every byte counter change).
const CreatedBy = "system:wifi-insights"

var (
	ssidStatsRE = regexp.MustCompile(`^Device\.WiFi\.SSID\.(\d+)\.Stats\.Bytes(Sent|Received)$`)
	apAssocRE   = regexp.MustCompile(`^Device\.WiFi\.AccessPoint\.(\d+)\.AssociatedDevice\.(\d+)\.([A-Za-z0-9_-]+)$`)
	apAssocObj  = regexp.MustCompile(`^Device\.WiFi\.AccessPoint\.\d+\.AssociatedDevice\.$`)
	hostRE      = regexp.MustCompile(`^Device\.Hosts\.Host\.(\d+)\.([A-Za-z0-9_-]+)$`)
	staRE       = regexp.MustCompile(`^Device\.WiFi\.DataElements\.Network\.Device\.(\d+)\.Radio\.(\d+)\.BSS\.(\d+)\.STA\.(\d+)\.([A-Za-z0-9_-]+)$`)
	staObj      = regexp.MustCompile(`^(Device\.WiFi\.DataElements\.Network\.Device\.\d+\.Radio\.\d+\.BSS\.\d+\.)STA\.$`)
	bssSSIDRE   = regexp.MustCompile(`^(Device\.WiFi\.DataElements\.Network\.Device\.\d+\.Radio\.\d+\.BSS\.\d+\.)SSID$`)
	// A mesh node's 1905 AL MAC and the MAC its backhaul link uses: field
	// data 2026-10-30 showed the Beacon in the FastMile's STA and Hosts
	// tables as if it were a client.
	meshNodeMACRE = regexp.MustCompile(`^Device\.WiFi\.DataElements\.Network\.Device\.\d+\.(ID|MultiAPDevice\.Backhaul\.MACAddress)$`)
	ethLinkMACRE  = regexp.MustCompile(`^Device\.Ethernet\.Link\.\d+\.MACAddress$`)
)

// ReadPaths lists what one sample reads, keyed off table objects in the
// device's discovered names (the FastMile omits most *NumberOfEntries
// counters, e.g. AccessPoint.{i}.AssociatedDeviceNumberOfEntries). SSID byte
// counters are explicit leaves; client tables are partial paths because
// their instances change between samples; each DataElements BSS's STA table
// is its own object instead of the whole DataElements tree (scan results,
// capability reports), since the FastMile drops CWMP sessions on large
// bursts.
func ReadPaths(names map[string]bool) []string {
	set := map[string]bool{}
	for path := range names {
		switch {
		case ssidStatsRE.MatchString(path), bssSSIDRE.MatchString(path), meshNodeMACRE.MatchString(path):
			set[path] = true
		case apAssocObj.MatchString(path), staObj.MatchString(path), path == "Device.Hosts.Host.":
			set[path] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// SSIDCounter is one SSID's cumulative byte counters. Down is what the AP
// sent to clients, Up what it received from them.
type SSIDCounter struct {
	Instance  int
	Name      string
	Role      string
	BytesDown int64
	BytesUp   int64
}

// Client is one client seen in a sample, merged by MAC from the
// AssociatedDevice, Hosts and EasyMesh STA tables.
type Client struct {
	MAC       string
	Hostname  string
	IPAddress string
	Wireless  bool
	Guest     bool
	SSID      int // SSID instance; 0 when unknown
	SSIDName  string
	Band      string
	MeshNode  int // DataElements Network.Device instance; 0 when not seen there
	SignalDBm *int
	// Cumulative per-association counters from EasyMesh STA (nil when the
	// CPE has no DataElements — AssociatedDevice carries no byte counters).
	BytesDown *int64
	BytesUp   *int64
}

// Snapshot is one parsed sample.
type Snapshot struct {
	SSIDs   []SSIDCounter
	Clients []Client
	// MeshMACs are MACs of mesh nodes (EasyMesh agents, linked satellites)
	// seen in this sample. They are never clients; Record also drops any
	// client rows stored for them before this was recognised.
	MeshMACs []string
}

// ExcludeMACs removes clients with any of these MACs and records them as
// mesh nodes (e.g. a linked satellite's own interface MACs).
func (s *Snapshot) ExcludeMACs(macs []string) {
	drop := map[string]bool{}
	for _, m := range s.MeshMACs {
		drop[m] = true
	}
	for _, raw := range macs {
		if m := NormalizeMAC(raw); m != "" && !drop[m] {
			drop[m] = true
			s.MeshMACs = append(s.MeshMACs, m)
		}
	}
	kept := s.Clients[:0]
	for _, c := range s.Clients {
		if !drop[c.MAC] {
			kept = append(kept, c)
		}
	}
	s.Clients = kept
}

// SatelliteMACs returns a mesh satellite's own interface MACs from its
// parameter cache (Device.Ethernet.Link.{i}.MACAddress), which its gateway
// may list in Hosts under a MAC its EasyMesh table does not carry.
func SatelliteMACs(cache map[string]parameters.CachedValue) []string {
	var out []string
	for path, v := range cache {
		if ethLinkMACRE.MatchString(path) {
			if m := NormalizeMAC(v.Value); m != "" {
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Counts returns wireless, wired and guest client totals.
func (s Snapshot) Counts() (wireless, wired, guest int) {
	for _, c := range s.Clients {
		if c.Wireless {
			wireless++
		} else {
			wired++
		}
		if c.Guest {
			guest++
		}
	}
	return
}

// Parse builds a snapshot from a GetParameterValues response (name→value).
// names and cache feed the per-instance Wi-Fi resolver so each client and
// counter is attributed to an SSID instance and role (primary/guest).
func Parse(values map[string]string, names map[string]bool, cache map[string]parameters.CachedValue) Snapshot {
	view := adapters.ResolveWiFiNetworks(names, cache)
	ssidByAP := map[int]adapters.WiFiNetwork{}
	ssidByInstance := map[int]adapters.WiFiNetwork{}
	ssidByName := map[string]adapters.WiFiNetwork{}
	for _, n := range view.Networks {
		ssidByInstance[n.Instance] = n
		if n.AccessPoint != "" {
			if ap, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(n.AccessPoint, "Device.WiFi.AccessPoint."), ".")); err == nil {
				ssidByAP[ap] = n
			}
		}
		if n.Name != nil && *n.Name != "" && n.Role != adapters.WiFiRoleDisabled {
			if _, dup := ssidByName[*n.Name]; !dup {
				ssidByName[*n.Name] = n
			}
		}
	}

	var meshMACs []string
	counters := map[int]*SSIDCounter{}
	assoc := map[[2]int]map[string]string{}
	hosts := map[int]map[string]string{}
	stas := map[[4]int]map[string]string{}
	bssSSID := map[string]string{}
	for name, value := range values {
		value = strings.TrimSpace(value)
		if m := ssidStatsRE.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			c := counters[n]
			if c == nil {
				c = &SSIDCounter{Instance: n}
				counters[n] = c
			}
			v, _ := strconv.ParseInt(value, 10, 64)
			if m[2] == "Sent" {
				c.BytesDown = v
			} else {
				c.BytesUp = v
			}
		} else if m := apAssocRE.FindStringSubmatch(name); m != nil {
			ap, _ := strconv.Atoi(m[1])
			j, _ := strconv.Atoi(m[2])
			key := [2]int{ap, j}
			if assoc[key] == nil {
				assoc[key] = map[string]string{}
			}
			assoc[key][m[3]] = value
		} else if m := hostRE.FindStringSubmatch(name); m != nil {
			h, _ := strconv.Atoi(m[1])
			if hosts[h] == nil {
				hosts[h] = map[string]string{}
			}
			hosts[h][m[2]] = value
		} else if m := staRE.FindStringSubmatch(name); m != nil {
			var key [4]int
			for i := range key {
				key[i], _ = strconv.Atoi(m[i+1])
			}
			if stas[key] == nil {
				stas[key] = map[string]string{}
			}
			stas[key][m[5]] = value
		} else if m := bssSSIDRE.FindStringSubmatch(name); m != nil {
			bssSSID[m[1]] = value
		} else if meshNodeMACRE.MatchString(name) {
			meshMACs = append(meshMACs, value)
		}
	}

	clients := map[string]*Client{}
	get := func(mac string) *Client {
		c := clients[mac]
		if c == nil {
			c = &Client{MAC: mac}
			clients[mac] = c
		}
		return c
	}
	for key, leaves := range assoc {
		mac := NormalizeMAC(leaves["MACAddress"])
		if mac == "" || leaves["Active"] == "0" || strings.EqualFold(leaves["Active"], "false") {
			continue
		}
		c := get(mac)
		c.Wireless = true
		if n, ok := ssidByAP[key[0]]; ok {
			c.SSID, c.Band = n.Instance, n.Band
			if n.Name != nil {
				c.SSIDName = *n.Name
			}
			if n.Role == adapters.WiFiRoleGuest {
				c.Guest = true
			}
		}
		if v, err := strconv.Atoi(leaves["SignalStrength"]); err == nil && v < 0 {
			c.SignalDBm = &v
		}
	}
	for key, leaves := range stas {
		mac := NormalizeMAC(leaves["MACAddress"])
		if mac == "" {
			continue
		}
		c := get(mac)
		c.Wireless = true
		c.MeshNode = key[0]
		if c.Hostname == "" {
			c.Hostname = leaves["Hostname"]
		}
		if c.IPAddress == "" {
			c.IPAddress = leaves["IPV4Address"]
		}
		if c.SSID == 0 {
			bss := "Device.WiFi.DataElements.Network.Device." + strconv.Itoa(key[0]) + ".Radio." + strconv.Itoa(key[1]) + ".BSS." + strconv.Itoa(key[2]) + "."
			if name := bssSSID[bss]; name != "" {
				c.SSIDName = name
				if n, ok := ssidByName[name]; ok {
					c.SSID = n.Instance
					if n.Role == adapters.WiFiRoleGuest {
						c.Guest = true
					}
				}
			}
		}
		if c.SignalDBm == nil {
			// EasyMesh SignalStrength is RCPI (0–220): dBm = RCPI/2 − 110.
			if rcpi, err := strconv.Atoi(leaves["SignalStrength"]); err == nil && rcpi > 0 && rcpi <= 220 {
				dbm := rcpi/2 - 110
				c.SignalDBm = &dbm
			}
		}
		if v, err := strconv.ParseInt(leaves["BytesSent"], 10, 64); err == nil {
			c.BytesDown = addCounter(c.BytesDown, v)
		}
		if v, err := strconv.ParseInt(leaves["BytesReceived"], 10, 64); err == nil {
			c.BytesUp = addCounter(c.BytesUp, v)
		}
	}
	for _, leaves := range hosts {
		mac := NormalizeMAC(leaves["PhysAddress"])
		if mac == "" {
			continue
		}
		active := leaves["Active"] == "1" || strings.EqualFold(leaves["Active"], "true")
		c, seen := clients[mac]
		if !seen {
			if !active {
				continue
			}
			c = get(mac)
			iface := strings.ToLower(leaves["InterfaceType"])
			c.Wireless = strings.Contains(iface, "wi") || strings.Contains(iface, "802.11")
		}
		if h := leaves["HostName"]; h != "" {
			c.Hostname = h
		}
		if ip := leaves["IPAddress"]; ip != "" {
			c.IPAddress = ip
		}
		if leaves["X_ALU-COM_GuestDevice"] == "1" || strings.EqualFold(leaves["X_ALU-COM_GuestDevice"], "true") {
			c.Guest = true
		}
		if c.Band == "" {
			c.Band = leaves["X_ALU-COM_WifiBand"]
		}
		if c.SignalDBm == nil {
			if v, err := strconv.Atoi(leaves["X_ALU-COM_WifiRSSI"]); err == nil && v < 0 {
				c.SignalDBm = &v
			}
		}
	}

	var snap Snapshot
	for _, c := range counters {
		if n, ok := ssidByInstance[c.Instance]; ok {
			c.Role = n.Role
			if n.Name != nil {
				c.Name = *n.Name
			}
		}
		snap.SSIDs = append(snap.SSIDs, *c)
	}
	sort.Slice(snap.SSIDs, func(i, j int) bool { return snap.SSIDs[i].Instance < snap.SSIDs[j].Instance })
	for _, c := range clients {
		snap.Clients = append(snap.Clients, *c)
	}
	sort.Slice(snap.Clients, func(i, j int) bool { return snap.Clients[i].MAC < snap.Clients[j].MAC })
	snap.ExcludeMACs(meshMACs)
	return snap
}

// addCounter sums a client's counters across BSSs (a client seen on two
// mesh nodes in one sample is counted once per association).
func addCounter(cur *int64, v int64) *int64 {
	if cur != nil {
		v += *cur
	}
	return &v
}

// NormalizeMAC returns a lower-case colon-separated MAC, or "" when the
// value is not a MAC.
func NormalizeMAC(raw string) string {
	hex := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			return r
		case r >= 'A' && r <= 'F':
			return r + ('a' - 'A')
		case r == ':' || r == '-' || r == '.':
			return -1
		}
		return '!'
	}, strings.TrimSpace(raw))
	if len(hex) != 12 || strings.Contains(hex, "!") || hex == "000000000000" {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			sb.WriteByte(':')
		}
		sb.WriteString(hex[i : i+2])
	}
	return sb.String()
}
