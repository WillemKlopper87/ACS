package parameters

import "strings"

// Subscriber identifier kinds. IMSI, ICCID, MSISDN, IMEI and eSIM EID tie a
// CPE to a subscriber (personal information under POPIA; RICA-registered
// identity for an MNO), so the API returns them in full only to a human
// superadmin and masks them for every other caller.
const (
	SubscriberIMSI   = "imsi"
	SubscriberICCID  = "iccid"
	SubscriberMSISDN = "msisdn"
	SubscriberIMEI   = "imei"
	SubscriberEID    = "eid"
	// SubscriberSecret is a SIM credential (USIM.PIN / PINCheck). It is
	// withheld from every caller, superadmin included: no lawful-access or
	// operational need requires the ACS to disclose a SIM PIN.
	SubscriberSecret = "secret"
)

// SubscriberIdentifierKind classifies a parameter path by its leaf name, or
// returns "" when the path is not a subscriber identifier. Matching is by
// leaf so vendor variants are covered, e.g. Nokia
// Device.Cellular.Interface.1.X_ALU-COM_CurrentUsedIMSI and
// X_ALU-COM_ESIM.ActiveProfileICCID / .Eid (FastMile 1.2501.00.0798).
func SubscriberIdentifierKind(path string) string {
	leaf := path
	if i := strings.LastIndex(path, "."); i >= 0 {
		leaf = path[i+1:]
	}
	upper := strings.ToUpper(leaf)
	switch {
	case upper == "":
		return ""
	case strings.Contains(path, ".USIM.") && (upper == "PIN" || upper == "PINCHECK" || upper == "PUK"):
		return SubscriberSecret
	case strings.HasSuffix(upper, "IMSI"):
		return SubscriberIMSI
	case strings.HasSuffix(upper, "ICCID"):
		return SubscriberICCID
	case strings.HasSuffix(upper, "MSISDN"):
		return SubscriberMSISDN
	case strings.HasSuffix(upper, "IMEI"), strings.HasSuffix(upper, "IMEISV"):
		return SubscriberIMEI
	case upper == "EID" || strings.HasSuffix(upper, "_EID"):
		return SubscriberEID
	}
	return ""
}

// MaskSubscriberIdentifier returns the value safe to show a caller without
// subscriber-identity access. IMSI keeps its MCC+MNC (network, not person)
// and IMEI its 8-digit TAC (device model); every kind keeps the last four
// characters so operators can tell SIMs apart. Secrets mask entirely.
func MaskSubscriberIdentifier(kind, value string) string {
	if kind == "" {
		return value
	}
	if kind == SubscriberSecret {
		return ""
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	keepHead := 0
	switch kind {
	case SubscriberIMSI:
		keepHead = 5 // MCC (3) + MNC (2; South African MNCs are 2 digits)
	case SubscriberIMEI:
		keepHead = 8 // TAC
	case SubscriberICCID:
		keepHead = 2 // "89" telecom industry prefix
	}
	const keepTail = 4
	if len(value) <= keepHead+keepTail {
		return strings.Repeat("•", len(value))
	}
	return value[:keepHead] + strings.Repeat("•", len(value)-keepHead-keepTail) + value[len(value)-keepTail:]
}

// RedactSubscriberIdentifiers returns a copy of cached with subscriber
// identifiers masked (and SIM secrets blanked) unless full is true; secrets
// are blanked even then. The input map is not modified.
func RedactSubscriberIdentifiers(cached map[string]CachedValue, full bool) map[string]CachedValue {
	out := make(map[string]CachedValue, len(cached))
	for name, v := range cached {
		if kind := SubscriberIdentifierKind(name); kind != "" && (!full || kind == SubscriberSecret) {
			v.Value = MaskSubscriberIdentifier(kind, v.Value)
		}
		out[name] = v
	}
	return out
}
