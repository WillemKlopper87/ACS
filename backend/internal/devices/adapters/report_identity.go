package adapters

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"acs/internal/devices"
	"acs/internal/parameters"
)

// Report identity: the "Current SSID" and "MAC Address" columns of the
// device export. Both read only cached values (the export never queues
// work) from explicit paths, instead of Device.WiFi.SSID.1.SSID and "the
// first cached key containing MACAddress" — which on the Nokia FastMile
// (2026-10-30) could be one band of the network, a guest SSID, a BSSID
// (Device.WiFi.SSID.{i}.MACAddress) or a Wi-Fi client's MAC.
//
// names is the device's discovered tree (path → writable). When it is
// empty (never discovered, or discovery marked stale) the cached keys
// stand in for it, so a device is still reported from what it sent.

// ReportSSID returns the primary Wi-Fi network name(s) for the export:
// TR-181 resolves every SSID and keeps the distinct names of the primary
// ones (FastMile: "SENT-5G" for SSID.1 + SSID.5), joined with " / " when
// the bands differ. "" when the device has no Wi-Fi of its own (e.g. a
// Beacon mesh satellite), no primary network, or its roles are not cached
// yet (no guess that could name a guest SSID). TR-098 keeps the canonical
// WLANConfiguration.1 path.
func ReportSSID(root string, names map[string]bool, cache map[string]parameters.CachedValue) string {
	names = namesOrCached(names, cache)
	if root == devices.DataModelRootIGD1 {
		path, _ := ResolvePath(root, WiFiSSID)
		if _, ok := names[path]; !ok {
			return ""
		}
		return strings.TrimSpace(cache[path].Value)
	}
	view := ResolveWiFiNetworks(names, cache)
	if !view.Complete {
		return ""
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range PrimaryWiFiNetworks(view) {
		if n.Name == nil || *n.Name == "" || seen[*n.Name] {
			continue
		}
		seen[*n.Name] = true
		out = append(out, *n.Name)
	}
	return strings.Join(out, " / ")
}

var (
	ethernetInterfaceMACRE = regexp.MustCompile(`^Device\.Ethernet\.Interface\.(\d+)\.MACAddress$`)
	ethernetLinkMACRE      = regexp.MustCompile(`^Device\.Ethernet\.Link\.(\d+)\.MACAddress$`)
	igdLANEthernetMACRE    = regexp.MustCompile(`^InternetGatewayDevice\.LANDevice\.(\d+)\.LANEthernetInterfaceConfig\.(\d+)\.MACAddress$`)
	igdWANConnectionMACRE  = regexp.MustCompile(`^InternetGatewayDevice\.WANDevice\.(\d+)\.WANConnectionDevice\.(\d+)\.WAN(?:IP|PPP)Connection\.(\d+)\.MACAddress$`)
)

// DeviceMACAddress returns the device's own MAC for the export, from the
// first family below that has a discovered, cached, non-zero value
// (lowest instance first within a family):
//
//	TR-181: Device.Ethernet.Interface.{i}.MACAddress — the physical port
//	        (FastMile: Interface.1, its only Ethernet interface), then
//	        Device.Ethernet.Link.{i}.MACAddress (Beacon 3.1 exposes only
//	        Link.1/Link.2).
//	TR-098: LANDevice.{i}.LANEthernetInterfaceConfig.{j}.MACAddress, then
//	        WANDevice.{i}.WANConnectionDevice.{j}.WAN{IP,PPP}Connection.{k}.MACAddress.
//
// Only Ethernet objects are considered: Device.WiFi.SSID.{i}.MACAddress is
// a per-SSID BSSID, AssociatedDevice/Hosts/DataElements entries are client
// stations. TR-181 has no standard DeviceInfo MAC, and the FastMile's WAN
// is cellular (no MAC), so the Ethernet port is the most stable identity
// both field devices expose. Whether it equals the label MAC has not been
// verified on the devices.
func DeviceMACAddress(root string, names map[string]bool, cache map[string]parameters.CachedValue) string {
	names = namesOrCached(names, cache)
	families := []*regexp.Regexp{ethernetInterfaceMACRE, ethernetLinkMACRE}
	if root == devices.DataModelRootIGD1 {
		families = []*regexp.Regexp{igdLANEthernetMACRE, igdWANConnectionMACRE}
	}
	for _, re := range families {
		type candidate struct {
			key   []int
			value string
		}
		var found []candidate
		for path := range names {
			m := re.FindStringSubmatch(path)
			if m == nil {
				continue
			}
			v, ok := cache[path]
			if !ok || !usableMAC(v.Value) {
				continue
			}
			key := make([]int, 0, len(m)-1)
			for _, s := range m[1:] {
				n, _ := strconv.Atoi(s)
				key = append(key, n)
			}
			found = append(found, candidate{key: key, value: strings.TrimSpace(v.Value)})
		}
		if len(found) == 0 {
			continue
		}
		sort.Slice(found, func(i, j int) bool {
			for k := range found[i].key {
				if found[i].key[k] != found[j].key[k] {
					return found[i].key[k] < found[j].key[k]
				}
			}
			return false
		})
		return found[0].value
	}
	return ""
}

func usableMAC(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && strings.Trim(v, "0:-.") != ""
}

func namesOrCached(names map[string]bool, cache map[string]parameters.CachedValue) map[string]bool {
	if len(names) > 0 {
		return names
	}
	out := make(map[string]bool, len(cache))
	for path := range cache {
		out[path] = false
	}
	return out
}
