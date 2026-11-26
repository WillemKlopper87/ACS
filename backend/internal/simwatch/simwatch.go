// Package simwatch judges whether the SIM in a CPE was replaced. It is pure:
// no SQL and no I/O, so every service that needs the rule can import it
// without gaining database privileges.
package simwatch

import (
	"sort"
	"strings"

	"acs/internal/devices/adapters"
	"acs/internal/parameters"
)

// CreatedBy marks jobs queued by the SIM watch sampler.
const CreatedBy = "system:sim-watch"

const (
	pathICCID      = "Device.Cellular.Interface.1.USIM.ICCID"
	pathIMSI       = "Device.Cellular.Interface.1.USIM.IMSI"
	pathVendorIMSI = "Device.Cellular.Interface.1.X_ALU-COM_CurrentUsedIMSI"
)

// Identity is the SIM identity as the CPE reports it. Empty means "no
// reading", never "no SIM".
type Identity struct{ ICCID, IMSI string }

// Empty reports whether nothing is known.
func (i Identity) Empty() bool { return i.ICCID == "" && i.IMSI == "" }

func clean(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || adapters.IsCellularSentinel(v) {
		return ""
	}
	return v
}

// ExtractIdentity reads interface 1 only. The IMSI falls back to the Nokia
// vendor leaf when the standard one is absent.
func ExtractIdentity(values map[string]string) Identity {
	id := Identity{ICCID: clean(values[pathICCID]), IMSI: clean(values[pathIMSI])}
	if id.IMSI == "" {
		id.IMSI = clean(values[pathVendorIMSI])
	}
	return id
}

// ReadPaths lists the identity paths present in the discovered names; nil
// when nothing is discovered or none is present, so a caller never falls
// back to paths the device may not have (a missing path fails the read).
func ReadPaths(names map[string]bool) []string {
	var out []string
	for _, p := range []string{pathICCID, pathIMSI, pathVendorIMSI} {
		if names[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Merge lets non-empty incoming fields override the cached ones.
func Merge(cached, incoming Identity) Identity {
	out := cached
	if incoming.ICCID != "" {
		out.ICCID = incoming.ICCID
	}
	if incoming.IMSI != "" {
		out.IMSI = incoming.IMSI
	}
	return out
}

// Network is the plan's PLMN form: the first five digits of the IMSI.
func Network(imsi string) string {
	if len(imsi) < 5 {
		return ""
	}
	return imsi[:5]
}

type Kind string

const (
	KindNone     Kind = "none"
	KindBaseline Kind = "baseline"
	KindNotice   Kind = "notice"
	KindAlert    Kind = "alert"
)

type Verdict struct {
	Kind    Kind
	Network string
}

// Judge compares the incoming identity, merged over the cached one, with the
// cached one. A nil allowed means every network is allowed.
func Judge(cached, incoming Identity, allowed func(imsi string) bool) Verdict {
	merged := Merge(cached, incoming)
	v := Verdict{Kind: KindNone, Network: Network(merged.IMSI)}
	if merged.Empty() {
		return v
	}
	notAllowed := merged.IMSI != "" && allowed != nil && !allowed(merged.IMSI)
	if cached.Empty() {
		v.Kind = KindBaseline
		if notAllowed {
			v.Kind = KindAlert
		}
		return v
	}
	changed := (cached.ICCID != "" && incoming.ICCID != "" && cached.ICCID != incoming.ICCID) ||
		(cached.IMSI != "" && incoming.IMSI != "" && cached.IMSI != incoming.IMSI)
	// An IMSI learned for the first time is checked against the allowed
	// networks like a first SIM: the network was never judged before.
	learned := cached.IMSI == "" && incoming.IMSI != ""
	if !changed {
		if learned && notAllowed {
			v.Kind = KindAlert
		}
		return v
	}
	v.Kind = KindNotice
	if notAllowed {
		v.Kind = KindAlert
	}
	return v
}

// Hints masks the identity for storage and display.
func Hints(i Identity) (iccidHint, imsiHint string) {
	if i.ICCID != "" {
		iccidHint = parameters.MaskSubscriberIdentifier(parameters.SubscriberICCID, i.ICCID)
	}
	if i.IMSI != "" {
		imsiHint = parameters.MaskSubscriberIdentifier(parameters.SubscriberIMSI, i.IMSI)
	}
	return iccidHint, imsiHint
}
