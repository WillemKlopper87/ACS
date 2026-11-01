import { describe, expect, it } from "vitest";
import type { Device } from "../api/types";
import {
  connectionRequestHost,
  deviceDisplayName,
  deviceLink,
  gatewayName,
  isMeshSatellite,
  satelliteReachabilityNote,
  satelliteRebootWarning,
} from "./mesh";

const fastMile: Device = {
  id: "gw-1",
  oui_serial: "A4FC77+3TG02241+CSC25430018F",
  manufacturer: "Nokia",
  oui: "A4FC77",
  product_class: "3TG02241",
  serial_number: "CSC25430018F",
  data_model_root: "DEVICE2",
  online_status: "ONLINE",
  connection_request_mode: "DIRECT_IPV4",
};

describe("mesh helpers", () => {
  it("detects a satellite from either the resolved link or the reported identity", () => {
    expect(isMeshSatellite({ gateway_device_id: "gw-1" })).toBe(true);
    expect(isMeshSatellite({ gateway_oui_serial: "A4FC77+3TG02241+CSC25430018F" })).toBe(true);
    expect(isMeshSatellite({})).toBe(false);
    expect(isMeshSatellite(null)).toBe(false);
  });

  it("names the gateway by label, model/serial, or reported identity", () => {
    expect(deviceDisplayName(fastMile)).toBe("3TG02241 · CSC25430018F");
    expect(deviceDisplayName({ ...fastMile, label: "Lab FastMile" })).toBe("Lab FastMile");
    expect(gatewayName(fastMile, {})).toBe("3TG02241 · CSC25430018F");
    expect(gatewayName(null, { gateway_oui_serial: "A4FC77+3TG02241+X" })).toBe("A4FC77+3TG02241+X");
    expect(gatewayName(null, {})).toBe("its gateway");
    expect(deviceLink("gw 1")).toBe("/devices/gw%201/overview");
  });

  it("warns that satellite recovery depends on the mesh backhaul", () => {
    const text = satelliteRebootWarning("Lab FastMile");
    expect(text).toContain("Lab FastMile");
    expect(text).toContain("8–11 minutes");
    expect(text).toContain("backhaul");
  });

  it("explains the Connection Request URL is on the gateway LAN", () => {
    expect(connectionRequestHost("http://192.168.1.48:7547/cr")).toBe("192.168.1.48");
    expect(connectionRequestHost("not a url")).toBe("");
    const note = satelliteReachabilityNote({ connection_request_url: "http://192.168.1.48:7547/cr" }, "Lab FastMile");
    expect(note).toContain("192.168.1.48");
    expect(note).toContain("LAN of Lab FastMile");
  });
});
