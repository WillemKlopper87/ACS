import { describe, expect, it } from "vitest";
import { deviceCapabilities, devicePath, parseDevicePath, visibleSections } from "./deviceSections";

describe("deviceCapabilities", () => {
  it("reads Wi-Fi and cellular support from the discovered data model, for any vendor", () => {
    const fastMile = { "Device.WiFi.SSID.1.SSID": true, "Device.Cellular.Interface.1.USIM.IMSI": false };
    expect(deviceCapabilities(fastMile, false)).toEqual({ discovered: true, wifi: true, cellular: true });

    // A TR-098 router with Wi-Fi and no cellular modem.
    const tr098 = { "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.SSID": true };
    expect(deviceCapabilities(tr098, false)).toEqual({ discovered: true, wifi: true, cellular: false });

    // A mesh satellite has no Wi-Fi tree of its own, but its Wi-Fi page
    // explains that the gateway reports it.
    expect(deviceCapabilities({ "Device.DeviceInfo.SerialNumber": false }, true)).toEqual({ discovered: true, wifi: true, cellular: false });
  });

  it("assumes support until the device has been discovered", () => {
    expect(deviceCapabilities(null, false)).toEqual({ discovered: false, wifi: true, cellular: true });
  });
});

describe("visibleSections", () => {
  it("hides sub-pages the device's data model cannot support", () => {
    const ids = visibleSections({ discovered: true, wifi: true, cellular: false }).map((s) => s.id);
    expect(ids).not.toContain("cellular");
    expect(ids).toContain("wifi");
    expect(ids[0]).toBe("overview");
  });

  it("marks the expert sub-pages as advanced", () => {
    const advanced = visibleSections({ discovered: true, wifi: true, cellular: true }).filter((s) => s.advanced).map((s) => s.id);
    expect(advanced).toEqual(["parameters", "remote"]);
  });
});

describe("device paths", () => {
  it("builds and parses /devices/<id>/<section>", () => {
    expect(devicePath("gw 1")).toBe("/devices/gw%201/overview");
    expect(devicePath("gw-1", "wifi")).toBe("/devices/gw-1/wifi");
    expect(parseDevicePath("/devices/gw%201/wifi")).toEqual({ id: "gw 1", section: "wifi" });
    expect(parseDevicePath("/devices/gw-1")).toEqual({ id: "gw-1", section: "overview" });
    expect(parseDevicePath("/devices/gw-1/nope")).toEqual({ id: "gw-1", section: "overview" });
    expect(parseDevicePath("/devices")).toBeNull();
    expect(parseDevicePath("/fleet")).toBeNull();
  });
});
