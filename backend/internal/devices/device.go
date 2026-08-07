// Package devices owns the durable device registry (design doc v3 §7.1,
// trimmed to the Phase 1 subset — see migrations/0001_devices.sql).
package devices

import "time"

// Device is a row of the devices table.
type Device struct {
	ID                                 string
	OUISerial                          string
	Manufacturer                       string
	OUI                                string
	ProductClass                       string
	SerialNumber                       string
	DataModelRoot                      string
	OnlineStatus                       string
	LastInformAt                       *time.Time
	LastInformEventCodes               []string
	ConnectionRequestURL               *string
	ConnectionRequestMode              string
	LastConnectionRequestAt            *time.Time
	LastConnectionRequestStatus        *string
	LastInformAfterConnectionRequestAt *time.Time
	FirstSeenAt                        time.Time
	LastUpdatedAt                      time.Time
	Tags                               []string
	CWMPAuthMode                       string
	DataModelRootConfirmedAt           *time.Time
	UDPConnectionRequestAddress        *string
	NATDetected                        *bool
	CustomerID                         *string
	Location                           *string
}

// Data model root values (design doc v3 §7.1's data_model_root check
// constraint) — set for real once parameter discovery (build plan
// nice-to-have backlog) succeeds against one root or the other, rather than
// left at UNKNOWN forever.
const (
	DataModelRootDevice2 = "DEVICE2"
	DataModelRootIGD1    = "IGD1"
	DataModelRootUnknown = "UNKNOWN"
)

// CWMP auth modes (design doc v3 §11.2) — recorded on every Inform from
// how *that* request actually authenticated, not assumed from server
// config, since mTLS and Digest can coexist on one fleet.
const (
	AuthModeMTLS   = "MTLS"
	AuthModeDigest = "DIGEST"
	AuthModeNone   = "NONE"
)

// Connection request reachability modes (design doc v3 §12.2).
const (
	ModeDirectIPv4       = "DIRECT_IPV4"
	ModeDirectIPv6       = "DIRECT_IPV6"
	ModeSTUNAnnexG       = "STUN_ANNEX_G"
	ModePeriodicFallback = "PERIODIC_FALLBACK_ONLY"
	ModeUnknown          = "UNKNOWN"
)
