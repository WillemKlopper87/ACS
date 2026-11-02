import { describe, expect, it } from "vitest";
import { editBody, formFor, impactOf, pairedNetworks, spareNetworks, type WiFiNetwork } from "./wifiEdit";

const net = (over: Partial<WiFiNetwork>): WiFiNetwork => ({
  instance: 1, ssid_object: "Device.WiFi.SSID.1.", access_point: "Device.WiFi.AccessPoint.1.", role: "primary",
  name: "Home", band: "2.4GHz", enabled: true, advertised: true, security_mode: "WPA2-Personal",
  name_writable: true, enable_writable: true, passphrase_writable: true, ...over,
});

describe("wifiEdit", () => {
  it("sends only what changed, with the paired band when chosen", () => {
    const n = net({});
    expect(editBody(n, { ...formFor(n), name: "Home2", alsoInstances: [5] })).toEqual({ name: "Home2", also_instances: [5] });
    expect(editBody(n, formFor(n))).toEqual({});
  });

  it("warns that renaming a busy network disconnects its clients", () => {
    const n = net({});
    const impact = impactOf(n, { ...formFor(n), name: "New", passphrase: "a-good-passphrase" }, { "1": 13, "5": 2 });
    expect(impact.disconnects).toBe(true);
    expect(impact.clients).toBe(13);
    expect(impact.summary).toMatch(/13 connected devices will disconnect and must rejoin "New" with the new passphrase/);
    expect(impact.problems).toEqual([]);
  });

  it("does not warn for a broadcast change", () => {
    const n = net({});
    const impact = impactOf(n, { ...formFor(n), advertised: false }, { "1": 3 });
    expect(impact.disconnects).toBe(false);
    expect(impact.summary).toMatch(/without disconnecting/);
  });

  it("blocks unsafe or incomplete changes", () => {
    const n = net({});
    expect(impactOf(n, { ...formFor(n), securityMode: "None" }, null).problems.join(" ")).toMatch(/open to anyone/);
    expect(impactOf(n, { ...formFor(n), passphrase: "short" }, null).problems.join(" ")).toMatch(/8 to 63/);
    const spare = net({ instance: 3, enabled: false, role: "disabled" });
    expect(impactOf(spare, { ...formFor(spare), enabled: true, name: "Office" }, null).problems.join(" ")).toMatch(/Set a passphrase to turn this network on/);
    expect(impactOf(n, formFor(n), null).problems).toContain("Nothing has changed.");
  });

  it("pairs the same network across bands and lists spare slots", () => {
    const all = [net({}), net({ instance: 5, band: "5GHz" }), net({ instance: 2, name: "Guest", role: "guest" }), net({ instance: 3, enabled: false, role: "disabled", name: "NOKIA-3" })];
    expect(pairedNetworks(all[0], all).map((n) => n.instance)).toEqual([5]);
    expect(spareNetworks(all).map((n) => n.instance)).toEqual([3]);
  });
});
