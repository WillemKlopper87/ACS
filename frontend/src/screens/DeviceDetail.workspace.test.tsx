import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { Device } from "../api/types";
import { DeviceDetail } from "./DeviceDetail";

// A gateway whose discovered model has Wi-Fi but no cellular modem, so the
// workspace must leave out "Cellular & SIM" whatever its vendor.
const router: Device = {
  id: "gw-1",
  oui_serial: "00E0FC+HG8245+SN0001",
  manufacturer: "Huawei",
  oui: "00E0FC",
  product_class: "HG8245",
  serial_number: "SN0001",
  label: "Branch router",
  data_model_root: "DEVICE2",
  online_status: "ONLINE",
  connection_request_mode: "DIRECT_IPV4",
  last_inform_at: "2026-10-30T15:00:00Z",
};

const calls: string[] = [];
vi.mock("../components/RemoteShell", () => ({ RemoteShell: () => null }));
vi.mock("../components/DeviceConsole", () => ({ DeviceConsole: () => null }));
vi.mock("../components/VPNTunnel", () => ({ VPNTunnel: () => null }));
vi.mock("../components/DeviceTenancy", () => ({ DeviceTenancy: () => null }));
vi.mock("../components/DeviceCompatibility", () => ({ DeviceCompatibility: () => null }));
vi.mock("../components/DeviceOnboardingPanel", () => ({ DeviceOnboardingPanel: () => <div>onboarding checks</div> }));
vi.mock("../components/WiFiInsights", () => ({ WiFiInsights: () => <div>wifi insights</div> }));
vi.mock("../components/SimPanel", () => ({ SimPanel: () => <div>sim panel</div> }));
vi.mock("../components/XmppPanel", () => ({ XmppPanel: () => <div>xmpp panel</div> }));
vi.mock("../auth/useAuth", () => ({ useAuth: () => ({ role: "noc" }) }));
vi.mock("../api/client", () => {
  const known: Record<string, () => Promise<unknown>> = {
    getDevice: () => Promise.resolve(router),
    listDeviceSatellites: () => Promise.resolve({ items: [], total: 0 }),
    getDeviceParameters: () => Promise.resolve({ parameters: {} }),
    getParameterNames: () => Promise.resolve({ names: { "Device.WiFi.SSID.1.SSID": true, "Device.DeviceInfo.SerialNumber": false } }),
    listDeviceJobs: () => Promise.resolve({ items: [{ command_key: "reboot_1", type: "REBOOT", status: "SUCCESS" }] }),
  };
  const api = new Proxy(known, {
    get: (target, prop: string) => {
      return (...args: unknown[]) => {
        calls.push(prop);
        void args;
        return target[prop]?.() ?? Promise.resolve({ items: [], recommendations: [], checks: [], paths: [], receivers: [] });
      };
    },
  });
  return { ApiError: class extends Error {}, api };
});

afterEach(() => {
  cleanup();
  calls.length = 0;
});

function open(section: Parameters<typeof DeviceDetail>[0]["section"]) {
  return render(
    <MemoryRouter>
      <DeviceDetail id="gw-1" section={section} onClose={() => {}} />
    </MemoryRouter>,
  );
}

describe("device workspace", () => {
  it("opens on an overview that loads only what it shows", async () => {
    open("overview");
    await waitFor(() => expect(screen.getByRole("heading", { name: /Branch router/ })).toBeTruthy());
    expect(await screen.findByText("reboot_1")).toBeTruthy();
    for (const notNeeded of ["getDeviceParameters", "listDeviceCredentials", "listDeviceUploads", "listFirmwareImages", "getDiagnosticsCapabilities"]) {
      expect(calls).not.toContain(notNeeded);
    }
    expect(calls).toContain("getParameterNames");
    expect(screen.queryByText("Object management")).toBeNull();
    expect(screen.queryByText("Ping")).toBeNull();
  });

  it("offers sub-pages from the device's data model, not its vendor", async () => {
    open("overview");
    const nav = await screen.findByRole("navigation", { name: "Device sections" });
    await waitFor(() => expect(within(nav).queryByRole("link", { name: "Cellular & SIM" })).toBeNull());
    expect(within(nav).getByRole("link", { name: "Wi-Fi" }).getAttribute("href")).toBe("/devices/gw-1/wifi");
    expect(within(nav).getByRole("link", { name: "Overview" }).getAttribute("aria-current")).toBe("page");
  });

  it("keeps the common actions in the header on every sub-page", async () => {
    for (const section of ["wifi", "firmware", "ownership"] as const) {
      const { unmount } = open(section);
      expect(await screen.findByRole("button", { name: "Reboot" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Connection Request" })).toBeTruthy();
      unmount();
    }
  });

  it("shows each sub-page's own tools only", async () => {
    let view = open("wifi");
    expect(await screen.findByText("wifi insights")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Set SSID" })).toBeTruthy();
    expect(screen.queryByText("sim panel")).toBeNull();
    view.unmount();

    view = open("diagnostics");
    expect(await screen.findByRole("button", { name: "Ping" })).toBeTruthy();
    expect(screen.getByText("onboarding checks")).toBeTruthy();
    expect(calls).toContain("getDiagnosticsCapabilities");
    view.unmount();

    calls.length = 0;
    view = open("parameters");
    expect(await screen.findByText("Object management")).toBeTruthy();
    expect(calls).toContain("getDeviceParameters");
    view.unmount();

    view = open("connectivity");
    expect(await screen.findByText("xmpp panel")).toBeTruthy();
    expect(screen.getByText("Device authentication credentials")).toBeTruthy();
    view.unmount();

    open("ownership");
    expect(await screen.findByRole("button", { name: "Save name" })).toBeTruthy();
  });
});
