package devices

import "time"

type ConnectionCheck struct {
	Category string `json:"category"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
	Action   string `json:"action,omitempty"`
}

// ConnectionChecks interprets recorded outcomes; it never invents results of
// a DNS/TLS probe from missing contact or tests a CPE-provided arbitrary URL.
func ConnectionChecks(device *Device, agent *UspAgent, now time.Time) []ConnectionCheck {
	checks := []ConnectionCheck{}
	if device.LastInformAt != nil {
		status := "observed"
		if now.Sub(*device.LastInformAt) > 24*time.Hour {
			status = "stale"
		}
		checks = append(checks, ConnectionCheck{"cwmp_contact", status, device.LastInformAt.UTC().Format(time.RFC3339), "Check the configured periodic Inform interval if contact is stale."})
	} else {
		checks = append(checks, ConnectionCheck{"cwmp_contact", "unknown", "No authenticated Inform recorded", "Check ACS URL, DNS, routing and the device's TLS trust store."})
	}
	auth := "unknown"
	if device.LastInformAt != nil {
		auth = "observed"
		if device.CWMPAuthMode == AuthModeNone {
			auth = "unauthenticated"
		}
	}
	checks = append(checks, ConnectionCheck{"cwmp_authentication", auth, device.CWMPAuthMode, "Use device-bound credentials or a trusted device certificate."})
	if device.NATDetected != nil && *device.NATDetected {
		checks = append(checks, ConnectionCheck{"nat", "observed", "Device reports NAT", "Use the supported STUN/UDP connection request mechanism, periodic Inform or persistent USP."})
	}
	if device.LastConnectionRequestStatus != nil {
		status := *device.LastConnectionRequestStatus
		actions := map[string]string{
			"DNS_FAILURE":        "Check the connection-request hostname and ACS resolver.",
			"TLS_FAILURE":        "Check certificate validity, hostname, trust chain and device clock.",
			"HTTP_401":           "Check connection-request credentials and authentication methods.",
			"HTTP_404":           "Check the connection-request URL path supplied by the CPE.",
			"TCP_FAILURE":        "Check routing, device listener, firewall and NAT reachability.",
			"TIMEOUT":            "Check reachability and the device's connection-request response time.",
			"BLOCKED_BY_POLICY":  "Check the configured device network allow-list.",
			"UDP_SENT_NO_INFORM": "Check the UDP address/NAT binding and whether the CPE sends an Inform after wake-up.",
			"HTTP_200":           "A wake-up was accepted; verify that a subsequent Inform arrived.",
		}
		checks = append(checks, ConnectionCheck{"connection_request", "observed", status, actions[status]})
	}
	if agent != nil {
		status := "disconnected"
		if agent.Connected {
			status = "connected"
		}
		checks = append(checks, ConnectionCheck{"usp_transport", status, agent.MTPKind, "If disconnected, check broker/WebSocket URL, TLS trust, endpoint credentials, keep-alive and reconnect configuration."})
		checks = append(checks, ConnectionCheck{"usp_last_seen", "observed", agent.LastSeenAt.UTC().Format(time.RFC3339), ""})
	}
	return checks
}
