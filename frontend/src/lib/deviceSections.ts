// The device workspace's sub-pages (docs/CONSOLE-UX-PROPOSAL-2026-10-30.md
// §2). Which ones a device shows comes from its discovered data model, not
// from its vendor: the console has to serve CPEs beyond the Nokia field
// units with different capabilities.

export type DeviceSectionId =
  | "overview"
  | "wifi"
  | "cellular"
  | "connectivity"
  | "diagnostics"
  | "firmware"
  | "parameters"
  | "ownership"
  | "remote";

export interface DeviceSection {
  id: DeviceSectionId;
  label: string;
  /** Expert tools: grouped apart in the sub-navigation. */
  advanced?: boolean;
  needs?: "wifi" | "cellular";
}

export const DEVICE_SECTIONS: DeviceSection[] = [
  { id: "overview", label: "Overview" },
  { id: "wifi", label: "Wi-Fi", needs: "wifi" },
  { id: "cellular", label: "Cellular & SIM", needs: "cellular" },
  { id: "connectivity", label: "Connectivity" },
  { id: "diagnostics", label: "Diagnostics" },
  { id: "firmware", label: "Firmware & files" },
  { id: "ownership", label: "Ownership" },
  { id: "parameters", label: "Parameters", advanced: true },
  { id: "remote", label: "Remote access", advanced: true },
];

export interface DeviceCapabilities {
  /** The device has a discovered data model. */
  discovered: boolean;
  wifi: boolean;
  cellular: boolean;
}

// Wi-Fi: TR-181 Device.WiFi, or TR-098 LANDevice.{i}.WLANConfiguration
// (LANDevice alone is Ethernet). Cellular: TR-181 Device.Cellular; TR-098
// has no standard cellular object, so such a device shows no cellular page.
const isWiFiPath = (n: string) =>
  n.startsWith("Device.WiFi.") || (n.startsWith("InternetGatewayDevice.LANDevice.") && n.includes(".WLANConfiguration."));
const isCellularPath = (n: string) => n.startsWith("Device.Cellular.");

/**
 * What a device's discovered names say it supports. Until discovery has
 * run, every sub-page is offered: the panels themselves report "not
 * supported" from the device's answers.
 */
export function deviceCapabilities(names: Record<string, boolean> | null, satellite: boolean): DeviceCapabilities {
  const paths = names ? Object.keys(names) : [];
  if (paths.length === 0) return { discovered: false, wifi: true, cellular: true };
  return { discovered: true, wifi: satellite || paths.some(isWiFiPath), cellular: paths.some(isCellularPath) };
}

export function visibleSections(caps: DeviceCapabilities): DeviceSection[] {
  return DEVICE_SECTIONS.filter((s) => !s.needs || caps[s.needs]);
}

export function devicePath(id: string, section: DeviceSectionId = "overview"): string {
  return `/devices/${encodeURIComponent(id)}/${section}`;
}

/** Parses /devices/<id>[/<section>]; an unknown section falls back to Overview. */
export function parseDevicePath(pathname: string): { id: string; section: DeviceSectionId } | null {
  const parts = pathname.split("/");
  if (parts[1] !== "devices" || !parts[2]) return null;
  const known = DEVICE_SECTIONS.some((s) => s.id === parts[3]);
  return { id: decodeURIComponent(parts[2]), section: known ? (parts[3] as DeviceSectionId) : "overview" };
}
