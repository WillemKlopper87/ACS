// Mesh satellite helpers (field test 2026-10-30). A satellite such as the
// Nokia WiFi Beacon 3.1 sits behind a gateway (FastMile 5G Gateway 2) and
// reports it through TR-111 Device.GatewayInfo; the backend resolves that to
// gateway_device_id. Several device actions behave differently for it: its
// Connection Request URL is on the gateway LAN, its Wi-Fi clients are
// reported by the gateway's EasyMesh controller, and a reboot only completes
// once the mesh backhaul rejoins.
import type { Device } from "../api/types";

/** True when the device reported an upstream gateway (linked or not). */
export function isMeshSatellite(device: Pick<Device, "gateway_device_id" | "gateway_oui_serial"> | null | undefined): boolean {
  return !!(device?.gateway_device_id || device?.gateway_oui_serial);
}

/** Operator-facing name for a device: label, else model + serial. */
export function deviceDisplayName(device: Pick<Device, "label" | "manufacturer" | "product_class" | "serial_number" | "oui_serial">): string {
  if (device.label?.trim()) return device.label.trim();
  const parts = [device.product_class, device.serial_number].map((p) => (p ?? "").trim()).filter(Boolean);
  return parts.length ? parts.join(" · ") : device.oui_serial;
}

/** Deep link to a device in the fleet view. */
export function deviceLink(id: string): string {
  return `/fleet?device=${encodeURIComponent(id)}`;
}

/** Name for the gateway: the loaded gateway row, else the reported identity. */
export function gatewayName(gateway: Device | null | undefined, satellite: Pick<Device, "gateway_oui_serial">): string {
  if (gateway) return deviceDisplayName(gateway);
  return satellite.gateway_oui_serial || "its gateway";
}

/** Extra confirmation text before rebooting or resetting a satellite. */
export function satelliteRebootWarning(gateway: string): string {
  return (
    `This unit is a mesh satellite of ${gateway}. After a reboot it only reconnects once its mesh backhaul ` +
    `to the gateway rejoins: in testing (Nokia Beacon 3.1) that took 8–11 minutes, and once needed manual ` +
    `recovery on site. It will show REBOOTING until it informs again.`
  );
}

/** Host part of a Connection Request URL, or "" when unparsable. */
export function connectionRequestHost(url: string | null | undefined): string {
  if (!url) return "";
  try {
    return new URL(url).hostname;
  } catch {
    return "";
  }
}

/** Note shown next to Connection Request / web GUI for a satellite. */
export function satelliteReachabilityNote(device: Pick<Device, "connection_request_url">, gateway: string): string {
  const host = connectionRequestHost(device.connection_request_url);
  return (
    `Mesh satellite: its Connection Request URL${host ? ` (${host})` : ""} and web GUI are on the LAN of ${gateway}, ` +
    `so the ACS cannot reach them directly. Queued work runs at its next periodic Inform.`
  );
}
