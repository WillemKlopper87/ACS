import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { Device } from "../api/types";
import { DeviceDetail } from "./DeviceDetail";

const { rebootDevice, refreshWifiClients } = vi.hoisted(() => ({
  rebootDevice: vi.fn().mockResolvedValue({ command_key: "REBOOT-1" }),
  refreshWifiClients: vi.fn().mockResolvedValue({ command_key: "GET-1" }),
}));

const gateway: Device = {
  id: "gw-1",
  oui_serial: "A4FC77+3TG02241+CSC25430018F",
  manufacturer: "Nokia",
  oui: "A4FC77",
  product_class: "3TG02241",
  serial_number: "CSC25430018F",
  label: "Lab FastMile",
  data_model_root: "DEVICE2",
  online_status: "ONLINE",
  connection_request_mode: "DIRECT_IPV4",
};
const beacon: Device = {
  ...gateway,
  id: "sat-1",
  oui_serial: "F89B6E+Beacon 3.1+ALCLB0000001",
  manufacturer: "ALCL",
  oui: "F89B6E",
  product_class: "Beacon 3.1",
  serial_number: "ALCLB0000001",
  label: undefined,
  connection_request_url: "http://192.168.1.48:7547/cr",
  connection_request_mode: "PERIODIC_FALLBACK_ONLY",
  gateway_device_id: "gw-1",
  gateway_oui_serial: "A4FC77+3TG02241+CSC25430018F",
};

// Panels unrelated to the mesh behaviour (xterm needs a real browser).
vi.mock("../components/RemoteShell", () => ({ RemoteShell: () => null }));
vi.mock("../components/DeviceConsole", () => ({ DeviceConsole: () => null }));
vi.mock("../components/VPNTunnel", () => ({ VPNTunnel: () => null }));
vi.mock("../components/DeviceTenancy", () => ({ DeviceTenancy: () => null }));
vi.mock("../components/DeviceCompatibility", () => ({ DeviceCompatibility: () => null }));
vi.mock("../components/DeviceOnboardingPanel", () => ({ DeviceOnboardingPanel: () => null }));
vi.mock("../auth/useAuth", () => ({ useAuth: () => ({ role: "superadmin" }) }));
vi.mock("../api/client", () => {
  const known: Record<string, (...args: string[]) => Promise<unknown>> = {
    getDevice: (id: string) => Promise.resolve(id === "gw-1" ? gateway : beacon),
    listDeviceSatellites: () => Promise.resolve({ items: [], total: 0 }),
    getDeviceParameters: () => Promise.resolve({ parameters: {} }),
    getParameterNames: () => Promise.resolve({ names: {} }),
    getWebGUIConfig: () => Promise.resolve({ configured: false }),
    rebootDevice,
    refreshWifiClients,
  };
  // Every other panel just needs an empty, well-formed answer.
  const api = new Proxy(known, {
    get: (target, prop: string) => target[prop] ?? (() => Promise.resolve({ items: [], recommendations: [], checks: [], paths: [], receivers: [] })),
  });
  return { ApiError: class extends Error {}, api };
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  rebootDevice.mockClear();
});

describe("DeviceDetail for a mesh satellite", () => {
  it("names the gateway, warns before reboot and redirects Wi-Fi client refresh", async () => {
    render(
      <MemoryRouter>
        <DeviceDetail id="sat-1" onClose={() => {}} />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByText(/Mesh satellite of/)).toBeTruthy());
    expect(screen.getAllByText(/192\.168\.1\.48/).length).toBeGreaterThan(0);
    await waitFor(() => expect(screen.getByText("Refresh WiFi clients on Lab FastMile")).toBeTruthy());
    expect(screen.queryByText("Refresh WiFi clients")).toBeNull();
    expect(screen.queryByText("Set SSID")).toBeNull();
    expect(screen.getByText(/Wi-Fi name and password for this mesh are set on its gateway/)).toBeTruthy();

    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    fireEvent.click(screen.getByText("Reboot"));
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(confirm.mock.calls[0][0]).toMatch(/mesh satellite of Lab FastMile[\s\S]*8–11 minutes/);
    expect(rebootDevice).not.toHaveBeenCalled();

    confirm.mockReturnValue(true);
    fireEvent.click(screen.getByText("Reboot"));
    await waitFor(() => expect(rebootDevice).toHaveBeenCalledWith("sat-1"));
  });

  it("reboots a standalone gateway without the mesh warning", async () => {
    render(
      <MemoryRouter>
        <DeviceDetail id="gw-1" onClose={() => {}} />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByText(/Device detail — Lab FastMile/)).toBeTruthy());
    const confirm = vi.spyOn(window, "confirm");
    fireEvent.click(screen.getByText("Reboot"));
    await waitFor(() => expect(rebootDevice).toHaveBeenCalledWith("gw-1"));
    expect(confirm).not.toHaveBeenCalled();
    expect(screen.getByText("Refresh WiFi clients")).toBeTruthy();
    expect(screen.getByText("Set SSID")).toBeTruthy();
  });
});
