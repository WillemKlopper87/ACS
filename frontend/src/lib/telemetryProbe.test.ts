import { describe, expect, it } from "vitest";
import { MAX_PROBE_PATHS, checkPaths, parsePaths, probePaths } from "./telemetryProbe";

const fastmile: Record<string, boolean> = Object.fromEntries(
  [
    "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRPCurrent",
    "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.SNRCurrent",
    "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.Band",
    "Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.RSRPCurrent",
    "Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.PhysicalCellID",
    "Device.Cellular.Interface.1.LastChange",
    "Device.Cellular.Interface.1.NetworkInUse",
    "Device.Cellular.RoamingStatus",
    "Device.IP.Interface.1.LastChange",
    "Device.IP.Interface.2.LastChange",
    "Device.Ethernet.Link.1.LastChange",
    "Device.Ethernet.Link.2.LastChange",
    "Device.DeviceInfo.UpTime",
    "Device.DeviceInfo.Location.1.Source",
    "Device.DeviceInfo.Location.1.AcquiredTime",
    "Device.DeviceInfo.Location.1.X_ALU-COM_TimeStamp",
    "Device.DeviceInfo.Location.1.X_ALU-COM_Latitude",
    "Device.DeviceInfo.Location.1.X_ALU-COM_Longitude",
    "Device.DeviceInfo.Location.1.X_ALU-COM_Altitude",
    "Device.WiFi.Radio.1.Enable",
  ].map((n) => [n, false]),
);

const coords = [
  "Device.DeviceInfo.Location.1.X_ALU-COM_Latitude",
  "Device.DeviceInfo.Location.1.X_ALU-COM_Longitude",
  "Device.DeviceInfo.Location.1.X_ALU-COM_Altitude",
];

describe("probePaths", () => {
  it("lists the signal, timer and location-metadata leaves without coordinates", () => {
    const p = probePaths(fastmile, { includeCoordinates: false });
    for (const want of [
      "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRPCurrent",
      "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.SNRCurrent",
      "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.Band",
      "Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.RSRPCurrent",
      "Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.PhysicalCellID",
      "Device.Cellular.Interface.1.LastChange",
      "Device.IP.Interface.1.LastChange",
      "Device.Ethernet.Link.1.LastChange",
      "Device.DeviceInfo.UpTime",
      "Device.Cellular.Interface.1.NetworkInUse",
      "Device.Cellular.RoamingStatus",
      "Device.DeviceInfo.Location.1.Source",
      "Device.DeviceInfo.Location.1.AcquiredTime",
      "Device.DeviceInfo.Location.1.X_ALU-COM_TimeStamp",
    ]) {
      expect(p).toContain(want);
    }
    for (const c of coords) expect(p).not.toContain(c);
    expect(p).not.toContain("Device.WiFi.Radio.1.Enable");
  });

  it("adds exactly the three coordinate leaves when asked", () => {
    const without = probePaths(fastmile, { includeCoordinates: false });
    const withCoords = probePaths(fastmile, { includeCoordinates: true });
    expect(withCoords.filter((x) => !without.includes(x)).sort()).toEqual([...coords].sort());
    expect(withCoords.length).toBe(without.length + 3);
  });

  it("drops preset paths the device does not have", () => {
    const names = { ...fastmile };
    delete names["Device.IP.Interface.2.LastChange"];
    expect(probePaths(names, { includeCoordinates: false })).not.toContain("Device.IP.Interface.2.LastChange");
  });

  it("returns nothing without discovered names or on a TR-098 tree", () => {
    expect(probePaths(null, { includeCoordinates: true })).toEqual([]);
    expect(probePaths({}, { includeCoordinates: true })).toEqual([]);
    expect(probePaths({ "InternetGatewayDevice.DeviceInfo.UpTime": false }, { includeCoordinates: true })).toEqual([]);
  });

  it("is sorted with no duplicates", () => {
    const p = probePaths(fastmile, { includeCoordinates: true });
    expect(p).toEqual([...p].sort((a, b) => a.localeCompare(b)));
    expect(new Set(p).size).toBe(p.length);
  });
});

describe("parsePaths", () => {
  it("trims, drops blanks and duplicates, keeps order", () => {
    expect(parsePaths("  a \n\nb\na\n")).toEqual(["a", "b"]);
  });
});

describe("checkPaths", () => {
  it("flags unknown and object paths", () => {
    expect(checkPaths(["Device.X", "Device.", "Device.DeviceInfo.UpTime"], fastmile)).toEqual({
      unknown: ["Device.X", "Device."],
      objectPaths: ["Device."],
      tooMany: false,
    });
  });

  it("cannot call anything unknown without names", () => {
    expect(checkPaths(["Device.X"], null).unknown).toEqual([]);
  });

  it("caps the count", () => {
    const n = (k: number) => Array.from({ length: k }, (_, i) => `Device.P${i}`);
    expect(MAX_PROBE_PATHS).toBe(60);
    expect(checkPaths(n(61), null).tooMany).toBe(true);
    expect(checkPaths(n(60), null).tooMany).toBe(false);
  });
});
