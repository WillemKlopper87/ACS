// Plain words for values the API sends as identifiers. One table, so a
// value reads the same wherever it is shown; the raw value stays available
// (as a tooltip) for anyone matching it against logs or the API.
const PLAIN: Record<string, string> = {
  // How a device can be reached for an immediate action.
  PERIODIC_FALLBACK_ONLY: "Reached at its next check-in",
  DIRECT_IPV4: "Reachable directly (IPv4)",
  DIRECT_IPV6: "Reachable directly (IPv6)",
  XMPP: "Reachable through XMPP",
  UDP_ANNEX_G: "Reachable through NAT (UDP)",
  UNKNOWN: "Not yet known",
  // Online status.
  ONLINE: "Online",
  OFFLINE: "Offline",
  UNREACHABLE: "Unreachable",
  REBOOTING: "Rebooting",
  // How recently a device checked in.
  under_5m: "In the last 5 minutes",
  under_1h: "In the last hour",
  under_24h: "In the last 24 hours",
  stale: "More than a day ago",
  never: "Never",
  // What a job does.
  SET_PARAMETER: "Set parameters",
  GET_PARAMETER: "Read parameters",
  CONNECTION_REQUEST: "Wake device",
  FIRMWARE_DOWNLOAD: "Firmware update",
  DIAGNOSTICS_PING: "Ping test",
  DIAGNOSTICS_TRACEROUTE: "Traceroute",
  DIAGNOSTICS_DOWNLOAD: "Download speed test",
  DIAGNOSTICS_UPLOAD: "Upload speed test",
  ADD_OBJECT: "Add object",
  DELETE_OBJECT: "Delete object",
  REBOOT: "Reboot",
  FACTORY_RESET: "Factory reset",
  SCHEDULE_INFORM: "Schedule a check-in",
  SET_PARAMETER_ATTRIBUTES: "Set notifications",
  GET_PARAMETER_ATTRIBUTES: "Read notifications",
  UPLOAD: "Fetch a file from the device",
  PARAMETER_DISCOVERY: "Discover data model",
};

export function plainLabel(value: string): string {
  return PLAIN[value] ?? value;
}
