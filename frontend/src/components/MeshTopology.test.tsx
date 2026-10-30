import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { Device } from "../api/types";
import { MeshTopology } from "./MeshTopology";
import { useMeshTopology } from "../lib/useMeshTopology";

const { getDevice, listDeviceSatellites } = vi.hoisted(() => ({
  getDevice: vi.fn(),
  listDeviceSatellites: vi.fn(),
}));
vi.mock("../api/client", () => ({ ApiError: class extends Error {}, api: { getDevice, listDeviceSatellites } }));

function device(overrides: Partial<Device>): Device {
  return {
    id: "dev",
    oui_serial: "OUI+PC+SN",
    manufacturer: "Nokia",
    oui: "A4FC77",
    product_class: "3TG02241",
    serial_number: "CSC25430018F",
    data_model_root: "DEVICE2",
    online_status: "ONLINE",
    connection_request_mode: "DIRECT_IPV4",
    ...overrides,
  };
}

const fastMile = device({ id: "gw-1", label: "Lab FastMile", last_inform_at: "2026-10-30T10:00:00Z" });
const beacon = device({
  id: "sat-1",
  manufacturer: "ALCL",
  oui: "F89B6E",
  product_class: "Beacon 3.1",
  serial_number: "ALCLB0000001",
  online_status: "REBOOTING",
  gateway_device_id: "gw-1",
  gateway_oui_serial: "A4FC77+3TG02241+CSC25430018F",
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("MeshTopology", () => {
  it("lists a gateway's satellites with model, state and last inform", () => {
    render(
      <MemoryRouter>
        <MeshTopology device={fastMile} mesh={{ gateway: null, satellites: [beacon] }} />
      </MemoryRouter>,
    );
    expect(screen.getByText(/1 mesh satellite behind this gateway/)).toBeTruthy();
    const link = screen.getByText("Beacon 3.1 · ALCLB0000001") as HTMLAnchorElement;
    expect(link.getAttribute("href")).toBe("/fleet?device=sat-1");
    expect(screen.getByText(/Nokia \(ALCL\) · Beacon 3\.1 · last inform/)).toBeTruthy();
    expect(screen.getByText("REBOOTING")).toBeTruthy();
  });

  it("links a satellite back to its gateway", () => {
    render(
      <MemoryRouter>
        <MeshTopology device={beacon} mesh={{ gateway: fastMile, satellites: [] }} />
      </MemoryRouter>,
    );
    expect(screen.getByText(/Mesh satellite of/)).toBeTruthy();
    expect((screen.getByText("Lab FastMile") as HTMLAnchorElement).getAttribute("href")).toBe("/fleet?device=gw-1");
  });

  it("shows the reported identity when the gateway is not readable", () => {
    render(
      <MemoryRouter>
        <MeshTopology device={{ ...beacon, gateway_device_id: undefined }} mesh={{ gateway: null, satellites: [] }} />
      </MemoryRouter>,
    );
    expect(screen.getByText("A4FC77+3TG02241+CSC25430018F")).toBeTruthy();
    expect(screen.getByText(/not registered in this ACS/)).toBeTruthy();
  });

  it("renders nothing for a standalone device", () => {
    const { container } = render(
      <MemoryRouter>
        <MeshTopology device={device({})} mesh={{ gateway: null, satellites: [] }} />
      </MemoryRouter>,
    );
    expect(container.textContent).toBe("");
  });
});

describe("useMeshTopology", () => {
  it("loads the gateway of a satellite and the satellites of a gateway", async () => {
    getDevice.mockResolvedValue(fastMile);
    listDeviceSatellites.mockResolvedValue({ items: [], total: 0 });
    const { result } = renderHook(() => useMeshTopology(beacon));
    await waitFor(() => expect(result.current.gateway?.id).toBe("gw-1"));
    expect(getDevice).toHaveBeenCalledWith("gw-1");

    listDeviceSatellites.mockResolvedValue({ items: [beacon], total: 1 });
    const gw = renderHook(() => useMeshTopology(fastMile));
    await waitFor(() => expect(gw.result.current.satellites.map((s) => s.id)).toEqual(["sat-1"]));
    expect(gw.result.current.gateway).toBeNull();
  });

  it("falls back to no gateway when it is outside the caller's scope", async () => {
    getDevice.mockRejectedValue(new Error("404"));
    listDeviceSatellites.mockResolvedValue({ items: [], total: 0 });
    const { result } = renderHook(() => useMeshTopology(beacon));
    await waitFor(() => expect(getDevice).toHaveBeenCalled());
    expect(result.current.gateway).toBeNull();
  });
});
