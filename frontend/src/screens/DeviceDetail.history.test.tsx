import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { Device } from "../api/types";
import { DeviceDetail } from "./DeviceDetail";

// Every parameter row says "history". On the Cellular page, as on the
// Parameters page, clicking one must open it: that page once passed the
// browser a handler that did nothing, so the rows promised history and gave
// none.
const modem: Device = {
  id: "cpe-1",
  oui_serial: "A8F7E0+FASTMILE+SN0001",
  manufacturer: "Nokia",
  oui: "A8F7E0",
  product_class: "FASTMILE",
  serial_number: "SN0001",
  label: "Lab CPE",
  data_model_root: "DEVICE2",
  online_status: "ONLINE",
  connection_request_mode: "DIRECT_IPV4",
  last_inform_at: "2026-10-30T15:00:00Z",
};

const RSRP = "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRPCurrent";
const historyCalls: Array<[string, string]> = [];

vi.mock("../components/RemoteShell", () => ({ RemoteShell: () => null }));
vi.mock("../components/DeviceConsole", () => ({ DeviceConsole: () => null }));
vi.mock("../components/VPNTunnel", () => ({ VPNTunnel: () => null }));
vi.mock("../components/DeviceTenancy", () => ({ DeviceTenancy: () => null }));
vi.mock("../components/DeviceCompatibility", () => ({ DeviceCompatibility: () => null }));
vi.mock("../components/DeviceOnboardingPanel", () => ({ DeviceOnboardingPanel: () => null }));
vi.mock("../components/WiFiInsights", () => ({ WiFiInsights: () => null }));
vi.mock("../components/WiFiNetworksPanel", () => ({ WiFiNetworksPanel: () => null }));
vi.mock("../components/SimPanel", () => ({ SimPanel: () => null }));
vi.mock("../components/XmppPanel", () => ({ XmppPanel: () => null }));
vi.mock("../auth/useAuth", () => ({ useAuth: () => ({ role: "noc" }) }));
vi.mock("../api/client", () => {
  const known: Record<string, (...args: string[]) => Promise<unknown>> = {
    getDevice: () => Promise.resolve(modem),
    getDeviceParameters: () =>
      Promise.resolve({ parameters: { [RSRP]: { value: "-101", updated_at: "2026-11-13T05:15:56Z", source: "GET_PARAMETER_VALUES" } } }),
    getParameterNames: () => Promise.resolve({ names: { [RSRP]: false } }),
    getParameterHistory: (id, name) => {
      historyCalls.push([id, name]);
      return Promise.resolve({
        name,
        items: [
          { value: "-99", type: "int", source: "GET_PARAMETER_VALUES", recorded_at: "2026-11-13T05:15:56Z" },
          { value: "-101", type: "int", source: "GET_PARAMETER_VALUES", recorded_at: "2026-10-30T09:56:22Z" },
        ],
      });
    },
  };
  const api = new Proxy(known, {
    get: (target, prop: string) => (...args: string[]) =>
      target[prop]?.(...args) ?? Promise.resolve({ items: [], recommendations: [], checks: [], paths: [], receivers: [] }),
  });
  return { ApiError: class extends Error {}, api };
});

afterEach(() => {
  cleanup();
  historyCalls.length = 0;
});

describe("parameter history on the device page", () => {
  for (const section of ["cellular", "parameters"] as const) {
    it(`opens a parameter's history on the ${section} page`, async () => {
      render(
        <MemoryRouter>
          <DeviceDetail id="cpe-1" section={section} onClose={() => {}} />
        </MemoryRouter>,
      );
      // Search lists each match by its full path, so no tree branch needs opening.
      fireEvent.change(await screen.findByLabelText("Search parameters"), { target: { value: "RSRPCurrent" } });
      const row = (await screen.findByText(RSRP)).closest(".param-row") as HTMLElement;
      fireEvent.click(row);
      await waitFor(() => expect(historyCalls).toEqual([["cpe-1", RSRP]]));
      expect(await screen.findByText("-99")).toBeTruthy();
      expect(row.textContent).toContain("hide history");
    });
  }
});
