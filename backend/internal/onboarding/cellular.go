package onboarding

import (
	"regexp"
	"sort"
	"strconv"

	"acs/internal/parameters"
)

// Write is one parameter the plan sets.
type Write struct {
	Name  string
	Value string
	Type  string
}

var (
	apnRE          = regexp.MustCompile(`^Device\.Cellular\.AccessPoint\.(\d+)\.APN$`)
	bootstrapAPNRE = regexp.MustCompile(`^Device\.Cellular\.Interface\.\d+\.X_[A-Za-z0-9_-]+_APNForBootstrap$`)
	iccidRE        = regexp.MustCompile(`^Device\.Cellular\.Interface\.\d+\.USIM\.ICCID$`)
	imsiRE         = regexp.MustCompile(`^Device\.Cellular\.Interface\.\d+\.USIM\.IMSI$`)
)

// CellularWrites resolves a plan's cellular profile against one device's
// discovered data model. Vendor fields (DNN, MTU, bootstrap APN) are found
// by suffix under the lowest AccessPoint instance that has an APN. Only
// writable paths are written, and only when the cached value differs: an
// APN write can drop the CPE's data link, and with it the ACS, so an
// unnecessary one is a risk with no benefit.
func CellularWrites(p CellularProfile, names map[string]bool, cache map[string]parameters.CachedValue) []Write {
	if p.empty() {
		return nil
	}
	ap := -1
	for name := range names {
		if m := apnRE.FindStringSubmatch(name); m != nil {
			if i, _ := strconv.Atoi(m[1]); ap < 0 || i < ap {
				ap = i
			}
		}
	}
	var out []Write
	add := func(path, value, typ string) {
		if path == "" || value == "" || !names[path] || cache[path].Value == value {
			return
		}
		out = append(out, Write{Name: path, Value: value, Type: typ})
	}
	if ap >= 0 {
		prefix := "Device.Cellular.AccessPoint." + strconv.Itoa(ap) + "."
		add(prefix+"APN", p.APN, "xsd:string")
		add(vendorLeaf(names, prefix, "_DNN"), p.DNN, "xsd:string")
		if p.MTU > 0 {
			add(vendorLeaf(names, prefix, "_APNMTU"), strconv.Itoa(p.MTU), "xsd:unsignedInt")
		}
	}
	for name := range names {
		if bootstrapAPNRE.MatchString(name) {
			add(name, p.BootstrapAPN, "xsd:string")
			break
		}
	}
	return out
}

// vendorLeaf finds <prefix>X_<vendor><suffix> among the discovered names.
func vendorLeaf(names map[string]bool, prefix, suffix string) string {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `X_[A-Za-z0-9_-]+` + regexp.QuoteMeta(suffix) + `$`)
	for name := range names {
		if re.MatchString(name) {
			return name
		}
	}
	return ""
}

// SIMIdentifiers returns the cached ICCID and IMSI, empty when not read.
func SIMIdentifiers(cache map[string]parameters.CachedValue) (iccid, imsi string) {
	for name, v := range cache {
		switch {
		case iccid == "" && iccidRE.MatchString(name):
			iccid = v.Value
		case imsi == "" && imsiRE.MatchString(name):
			imsi = v.Value
		}
	}
	return iccid, imsi
}

// SIMReadPaths names the SIM identifiers to read, from the discovered model
// when there is one, else the TR-181 defaults.
func SIMReadPaths(names map[string]bool) []string {
	var out []string
	for name := range names {
		if iccidRE.MatchString(name) || imsiRE.MatchString(name) {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return []string{"Device.Cellular.Interface.1.USIM.ICCID", "Device.Cellular.Interface.1.USIM.IMSI"}
	}
	sort.Strings(out)
	return out
}
