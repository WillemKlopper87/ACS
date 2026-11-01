package adapters

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"acs/internal/parameters"
)

// Wi-Fi network roles. Only RolePrimary networks receive operator/BSS
// "set the Wi-Fi name/password" writes; guest and unbound SSIDs (e.g. an
// EasyMesh backhaul BSS that no fronthaul AccessPoint references) must not
// be renamed as a side effect.
const (
	WiFiRolePrimary  = "primary"
	WiFiRoleGuest    = "guest"
	WiFiRoleUnbound  = "unbound"
	WiFiRoleDisabled = "disabled"
)

// WiFiNetwork is one TR-181 SSID joined with the AccessPoint that serves
// it and the Radio below it. Every *Path field is a concrete parameter path
// present in the device's discovery (empty when the device lacks it), so
// writes never target guessed instances.
type WiFiNetwork struct {
	Instance        int     `json:"instance"`
	SSIDObject      string  `json:"ssid_object"`
	AccessPoint     string  `json:"access_point,omitempty"`
	Radio           string  `json:"radio,omitempty"`
	Name            *string `json:"name,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty"`
	Advertised      *bool   `json:"advertised,omitempty"`
	Guest           *bool   `json:"guest,omitempty"`
	Band            string  `json:"band,omitempty"`
	SecurityMode    string  `json:"security_mode,omitempty"`
	Role            string  `json:"role"`
	NamePath        string  `json:"name_path,omitempty"`
	EnablePath      string  `json:"enable_path,omitempty"`
	PassphrasePath  string  `json:"passphrase_path,omitempty"`
	NameWritable    bool    `json:"name_writable"`
	EnableWritable  bool    `json:"enable_writable"`
	PassphraseWrite bool    `json:"passphrase_writable"`
	// APInferred is true when the AccessPoint was paired by equal instance
	// number because its SSIDReference value is not cached yet.
	APInferred bool `json:"ap_inferred,omitempty"`
}

// WiFiNetworks is the resolved view plus the reads needed to complete it.
type WiFiNetworks struct {
	Networks []WiFiNetwork `json:"networks"`
	// Complete is false while values that decide a network's role (guest
	// flag, AccessPoint→SSID reference, radio band) are discovered but not
	// cached; callers that write must refresh RefreshPaths first.
	Complete     bool     `json:"complete"`
	RefreshPaths []string `json:"refresh_paths"`
}

var (
	ssidLeafRE = regexp.MustCompile(`^Device\.WiFi\.SSID\.(\d+)\.SSID$`)
	apRefRE    = regexp.MustCompile(`^Device\.WiFi\.AccessPoint\.(\d+)\.SSIDReference$`)
	guestRefRE = regexp.MustCompile(`^Device\.WiFi\.X_[^.]+\.(\d+)\.SSIDReference$`) // e.g. Nokia X_ALU-COM_WLANForGuest
)

// ResolveWiFiNetworks builds the TR-181 Wi-Fi network view from discovered
// names (path → writable) and cached values. It never assumes instance 1:
// field test 2026-10-30 (Nokia FastMile 5G Gateway 2, an EasyMesh
// controller) showed multiple SSIDs per band plus guest SSIDs, so the old
// fixed Device.WiFi.SSID.1 path could rename only one band or a guest
// network. TR-098 (InternetGatewayDevice) devices are not handled here.
func ResolveWiFiNetworks(names map[string]bool, cache map[string]parameters.CachedValue) WiFiNetworks {
	out := WiFiNetworks{Networks: []WiFiNetwork{}, Complete: true, RefreshPaths: []string{}}
	value := func(path string) (string, bool) {
		v, ok := cache[path]
		return strings.TrimSpace(v.Value), ok
	}
	present := func(path string) bool { _, ok := names[path]; return ok }
	need := func(path string) {
		if !present(path) {
			return
		}
		if _, ok := cache[path]; !ok {
			out.Complete = false
			out.RefreshPaths = append(out.RefreshPaths, path)
		}
	}

	// AccessPoint → SSID references and guest-WLAN references.
	apBySSID := map[string]string{}
	apInstances := map[int]bool{}
	guestSSIDs := map[string]bool{}
	for path := range names {
		if m := apRefRE.FindStringSubmatch(path); m != nil {
			n, _ := strconv.Atoi(m[1])
			apInstances[n] = true
			need(path)
			if ref, ok := value(path); ok && ref != "" {
				apBySSID[normalizeObjectRef(ref)] = "Device.WiFi.AccessPoint." + m[1] + "."
			}
		}
		if m := guestRefRE.FindStringSubmatch(path); m != nil && !strings.HasPrefix(path, "Device.WiFi.AccessPoint.") {
			need(path)
			if ref, ok := value(path); ok && ref != "" {
				guestSSIDs[normalizeObjectRef(ref)] = true
			}
		}
	}

	for path := range names {
		m := ssidLeafRE.FindStringSubmatch(path)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		obj := "Device.WiFi.SSID." + m[1] + "."
		net := WiFiNetwork{Instance: n, SSIDObject: obj, NamePath: path, NameWritable: names[path]}
		need(path)
		if v, ok := value(path); ok {
			net.Name = &v
		}
		if p := obj + "Enable"; present(p) {
			net.EnablePath, net.EnableWritable = p, names[p]
			need(p)
			net.Enabled = boolValue(value(p))
		}
		// Vendor guest flag, e.g. Nokia X_ALU-COM_IsGuestSSID.
		for p := range names {
			if strings.HasPrefix(p, obj+"X_") && strings.HasSuffix(p, "IsGuestSSID") {
				need(p)
				net.Guest = boolValue(value(p))
			}
		}
		if guestSSIDs[obj] {
			t := true
			net.Guest = &t
		}
		if p := obj + "LowerLayers"; present(p) {
			need(p)
			if ref, ok := value(p); ok && ref != "" {
				net.Radio = normalizeObjectRef(strings.Split(ref, ",")[0])
				if bp := net.Radio + "OperatingFrequencyBand"; present(bp) {
					need(bp)
					if band, ok := value(bp); ok {
						net.Band = band
					}
				}
			}
		}

		ap := apBySSID[obj]
		if ap == "" && apInstances[n] && !hasCachedRef(apRefRE, n, cache) {
			// Reference not cached yet: pair by instance (common layout),
			// flagged so callers know it is an inference.
			ap, net.APInferred = "Device.WiFi.AccessPoint."+m[1]+".", true
		}
		if ap != "" {
			net.AccessPoint = ap
			if p := ap + "Security.KeyPassphrase"; present(p) {
				net.PassphrasePath, net.PassphraseWrite = p, names[p]
			}
			if p := ap + "SSIDAdvertisementEnabled"; present(p) {
				need(p)
				net.Advertised = boolValue(value(p))
			}
			if p := ap + "Security.ModeEnabled"; present(p) {
				need(p)
				if v, ok := value(p); ok {
					net.SecurityMode = v
				}
			}
		}

		switch {
		case net.Guest != nil && *net.Guest:
			net.Role = WiFiRoleGuest
		case net.AccessPoint == "":
			net.Role = WiFiRoleUnbound
		case net.Enabled != nil && !*net.Enabled:
			// Nokia FastMile ships extra factory SSIDs (NOKIA-xxxx-3/4/7/8);
			// renaming "the Wi-Fi" must not touch networks that are off.
			net.Role = WiFiRoleDisabled
		default:
			net.Role = WiFiRolePrimary
		}
		out.Networks = append(out.Networks, net)
	}
	sort.Slice(out.Networks, func(i, j int) bool { return out.Networks[i].Instance < out.Networks[j].Instance })
	sort.Strings(out.RefreshPaths)
	return out
}

// PrimaryWiFiNetworks returns the fronthaul networks an operator means by
// "the Wi-Fi": role primary, typically one per band on a dual-band CPE.
func PrimaryWiFiNetworks(v WiFiNetworks) []WiFiNetwork {
	var out []WiFiNetwork
	for _, n := range v.Networks {
		if n.Role == WiFiRolePrimary {
			out = append(out, n)
		}
	}
	return out
}

// WiFiNetworkReadPaths lists the non-secret values ResolveWiFiNetworks uses,
// for a targeted refresh read (never passphrases/keys).
func WiFiNetworkReadPaths(names map[string]bool) []string {
	var out []string
	for path := range names {
		if !strings.HasPrefix(path, "Device.WiFi.") || strings.HasSuffix(path, ".") {
			continue
		}
		leaf := lastPathSegment(path)
		switch {
		case strings.Contains(path, ".AssociatedDevice."), strings.Contains(path, ".DataElements."):
			continue
		case ssidLeafRE.MatchString(path), apRefRE.MatchString(path), guestRefRE.MatchString(path):
		case strings.HasPrefix(path, "Device.WiFi.SSID.") && (leaf == "Enable" || leaf == "LowerLayers" || leaf == "Status" || strings.HasSuffix(leaf, "IsGuestSSID")):
		case strings.HasPrefix(path, "Device.WiFi.AccessPoint.") && (leaf == "SSIDAdvertisementEnabled" || leaf == "Enable" || strings.HasSuffix(path, ".Security.ModeEnabled")):
		case strings.HasPrefix(path, "Device.WiFi.Radio.") && (leaf == "OperatingFrequencyBand" || leaf == "Channel"):
		default:
			continue
		}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func normalizeObjectRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref != "" && !strings.HasSuffix(ref, ".") {
		ref += "."
	}
	return ref
}

func hasCachedRef(re *regexp.Regexp, instance int, cache map[string]parameters.CachedValue) bool {
	for path := range cache {
		if m := re.FindStringSubmatch(path); m != nil && m[1] == strconv.Itoa(instance) {
			return true
		}
	}
	return false
}

func boolValue(v string, ok bool) *bool {
	if !ok {
		return nil
	}
	b := v == "1" || strings.EqualFold(v, "true")
	return &b
}
