import { describe, expect, it } from "vitest";
import type { ParameterCache } from "../api/types";
import { HEALTH_PATHS, deviceHealth, fmtUptime, signalQuality } from "./deviceHealth";

const cache = (values: Record<string, string>): ParameterCache =>
  Object.fromEntries(Object.entries(values).map(([k, value]) => [k, { value, updated_at: "2026-11-14T00:00:00Z", source: "inform" }]));

describe("deviceHealth", () => {
  it("reads a TR-181 gateway", () => {
    const h = deviceHealth(
      cache({
        "Device.DeviceInfo.SoftwareVersion": "2.3.1",
        "Device.DeviceInfo.UpTime": "90061",
        "Device.Cellular.Interface.1.RSRP": "-97",
        "Device.Cellular.Interface.1.CurrentAccessTechnology": "NR5G-SA",
        "Device.WiFi.AccessPoint.1.AssociatedDeviceNumberOfEntries": "3",
        "Device.WiFi.AccessPoint.5.AssociatedDeviceNumberOfEntries": "4",
      }),
    );
    expect(h).toEqual({ firmware: "2.3.1", uptimeSeconds: 90061, rsrp: -97, signal: "fair", technology: "NR5G-SA", wifiClients: 7 });
  });

  it("reads a TR-098 device", () => {
    expect(deviceHealth(cache({ "InternetGatewayDevice.DeviceInfo.SoftwareVersion": "V1R2" })).firmware).toBe("V1R2");
  });

  it("shows nothing for what the device did not report", () => {
    // No cellular radio, no Wi-Fi tree (a mesh satellite), no measurement.
    const h = deviceHealth(cache({ "Device.DeviceInfo.SoftwareVersion": "1.0", "Device.Cellular.Interface.1.RSRP": "0" }));
    expect(h.rsrp).toBeUndefined();
    expect(h.signal).toBeUndefined();
    expect(h.wifiClients).toBeUndefined();
    expect(h.uptimeSeconds).toBeUndefined();
    expect(deviceHealth({})).toEqual({ firmware: undefined, technology: undefined });
  });

  it("reports zero Wi-Fi clients as zero, not as unknown", () => {
    expect(deviceHealth(cache({ "Device.WiFi.AccessPoint.1.AssociatedDeviceNumberOfEntries": "0" })).wifiClients).toBe(0);
  });

  it("asks for exact names only", () => {
    expect(HEALTH_PATHS).toContain("Device.DeviceInfo.SoftwareVersion");
    expect(HEALTH_PATHS.every((p) => !p.endsWith("."))).toBe(true);
  });
});

describe("signal and uptime", () => {
  it("bands RSRP", () => {
    expect(signalQuality(-80)).toBe("good");
    expect(signalQuality(-90)).toBe("good");
    expect(signalQuality(-105)).toBe("fair");
    expect(signalQuality(-112)).toBe("poor");
  });

  it("writes uptime in two units", () => {
    expect(fmtUptime(90061)).toBe("1d 1h");
    expect(fmtUptime(7260)).toBe("2h 1m");
    expect(fmtUptime(30)).toBe("1m");
  });
});
