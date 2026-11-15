import type { ParameterCache } from "../api/types";

// What the device overview shows at a glance, read from the parameter
// cache. Only standard TR-181 / TR-098 names are used, so a device that
// keeps a value somewhere vendor-specific simply shows no figure for it
// rather than a wrong one.

const ROOTS = ["Device.", "InternetGatewayDevice."];
const ACCESS_POINTS = [1, 2, 3, 4, 5, 6, 7, 8];

// The exact names to ask the API for: the full cache of a gateway is
// thousands of parameters, and the overview needs a dozen.
export const HEALTH_PATHS: string[] = [
  ...ROOTS.flatMap((r) => [`${r}DeviceInfo.SoftwareVersion`, `${r}DeviceInfo.UpTime`]),
  "Device.Cellular.Interface.1.RSRP",
  "Device.Cellular.Interface.1.CurrentAccessTechnology",
  ...ACCESS_POINTS.map((i) => `Device.WiFi.AccessPoint.${i}.AssociatedDeviceNumberOfEntries`),
];

export type SignalQuality = "good" | "fair" | "poor";

export interface DeviceHealth {
  firmware?: string;
  uptimeSeconds?: number;
  rsrp?: number;
  signal?: SignalQuality;
  technology?: string;
  wifiClients?: number;
}

function first(params: ParameterCache, names: string[]): string | undefined {
  for (const n of names) {
    const v = params[n]?.value;
    if (v !== undefined && v !== "") return v;
  }
  return undefined;
}

// RSRP bands in common use for LTE/5G fixed wireless: above -90 dBm is a
// good link, down to -105 usable, below that marginal.
export function signalQuality(rsrp: number): SignalQuality {
  if (rsrp >= -90) return "good";
  if (rsrp >= -105) return "fair";
  return "poor";
}

export function deviceHealth(params: ParameterCache): DeviceHealth {
  const out: DeviceHealth = {};
  out.firmware = first(params, ROOTS.map((r) => `${r}DeviceInfo.SoftwareVersion`));

  const uptime = Number(first(params, ROOTS.map((r) => `${r}DeviceInfo.UpTime`)));
  if (Number.isFinite(uptime) && uptime > 0) out.uptimeSeconds = uptime;

  const rsrp = Number(params["Device.Cellular.Interface.1.RSRP"]?.value);
  // Devices report 0 or an empty value when there is no measurement.
  if (Number.isFinite(rsrp) && rsrp < 0) {
    out.rsrp = rsrp;
    out.signal = signalQuality(rsrp);
  }
  out.technology = first(params, ["Device.Cellular.Interface.1.CurrentAccessTechnology"]);

  let clients = 0;
  let reported = false;
  for (const i of ACCESS_POINTS) {
    const v = params[`Device.WiFi.AccessPoint.${i}.AssociatedDeviceNumberOfEntries`]?.value;
    if (v === undefined || v === "") continue;
    const n = Number(v);
    if (Number.isFinite(n)) {
      clients += n;
      reported = true;
    }
  }
  if (reported) out.wifiClients = clients;
  return out;
}

export function fmtUptime(seconds: number): string {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${Math.max(m, 1)}m`;
}
