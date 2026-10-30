import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { WiFiInsightsData } from "../api/client";
import { WiFiInsights } from "./WiFiInsights";

const { getWiFiInsights } = vi.hoisted(() => ({ getWiFiInsights: vi.fn() }));
vi.mock("../api/client", () => ({ ApiError: class extends Error {}, api: { getWiFiInsights } }));

const sample: WiFiInsightsData = {
  device_id: "gw-1",
  window: "2h",
  since: "2026-10-30T13:00:00Z",
  bucket_seconds: 300,
  last_sample_at: "2026-10-30T14:55:00Z",
  total_bytes_down: 3 * 1024 * 1024 * 1024,
  total_bytes_up: 200 * 1024 * 1024,
  bandwidth: [
    { at: "2026-10-30T14:45:00Z", bytes_down: 1000, bytes_up: 100, bps_down: 4_000_000, bps_up: 500_000 },
    { at: "2026-10-30T14:50:00Z", bytes_down: 2000, bytes_up: 200, bps_down: 12_400_000, bps_up: 900_000 },
  ],
  client_counts: [
    { at: "2026-10-30T14:45:00Z", wireless: 5, wired: 1, guest: 0 },
    { at: "2026-10-30T14:50:00Z", wireless: 6, wired: 1, guest: 1 },
  ],
  top_ssids: [
    { instance: 5, name: "SENT-5G", role: "primary", bytes_down: 2 * 1024 ** 3, bytes_up: 100 * 1024 ** 2 },
    { instance: 2, name: "SENT-5G_Guest", role: "guest", bytes_down: 10 * 1024 ** 2, bytes_up: 1024 ** 2 },
  ],
  top_clients: [{ mac: "AA:BB:CC:00:11:22", hostname: "Galaxy-S24", vendor: "Samsung", bytes_down: 1024 ** 3, bytes_up: 50 * 1024 ** 2 }],
  client_stats: { new: 2, returning: 5, private_mac: 3, average_session_seconds: 3 * 3600 + 7 * 60 },
  vendors: [{ name: "Private", count: 3 }, { name: "Samsung", count: 2 }],
  os_inferred: [{ name: "Android", count: 2 }],
  clients: [
    {
      mac: "AA:BB:CC:00:11:22", hostname: "Galaxy-S24", ip_address: "192.168.1.20", ssid_instance: 5, ssid_name: "SENT-5G",
      band: "5GHz", wireless: true, guest: false, private_mac: false, vendor: "Samsung", os_inferred: "Android",
      signal_dbm: -58, first_seen_at: "2026-10-30T09:00:00Z", last_seen_at: "2026-10-30T14:55:00Z", connected: true,
      sessions: 2, connected_seconds: 7200,
    },
    {
      mac: "DA:00:00:00:00:01", hostname: "", ip_address: "192.168.1.31", ssid_name: "SENT-5G_Guest", band: "2.4GHz",
      wireless: true, guest: true, private_mac: true, vendor: "Private", os_inferred: "", signal_dbm: null,
      first_seen_at: "2026-10-30T14:00:00Z", last_seen_at: "2026-10-30T14:20:00Z", connected: false, sessions: 1,
      connected_seconds: 1200,
    },
  ],
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("WiFiInsights", () => {
  it("summarises the window and lists top SSIDs, clients and devices", async () => {
    getWiFiInsights.mockResolvedValue(sample);
    render(<WiFiInsights deviceId="gw-1" />);

    await waitFor(() => expect(screen.getByText("3.0 GiB")).toBeTruthy());
    expect(getWiFiInsights).toHaveBeenCalledWith("gw-1", "2h");
    expect(screen.getByText("200 MiB")).toBeTruthy();
    expect(screen.getByText("3h 7m")).toBeTruthy();
    const stats = screen.getByRole("list", { name: "Wi-Fi summary" });
    expect(within(stats).getByText("Connected now").previousElementSibling?.textContent).toBe("1");
    expect(within(stats).getByText("Private MACs").previousElementSibling?.textContent).toBe("3");

    expect(screen.getByRole("img", { name: /Bandwidth, 2 samples/ })).toBeTruthy();
    expect(screen.getByRole("img", { name: /Clients, 2 samples/ })).toBeTruthy();
    const ssids = screen.getByRole("list", { name: "Top SSIDs" });
    expect(within(ssids).getByText("SENT-5G_Guest")).toBeTruthy();
    expect(within(ssids).getByText("guest")).toBeTruthy();
    expect(screen.getByRole("list", { name: "Client OS (inferred)" })).toBeTruthy();

    const table = screen.getByRole("table", { name: "Wi-Fi clients" });
    expect(within(table).getByText("Galaxy-S24")).toBeTruthy();
    expect(within(table).getByText("-58 dBm")).toBeTruthy();
    expect(within(table).getByText("private")).toBeTruthy();
  });

  it("refetches when the window changes", async () => {
    getWiFiInsights.mockResolvedValue(sample);
    render(<WiFiInsights deviceId="gw-1" />);
    await waitFor(() => expect(getWiFiInsights).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("radio", { name: "1 day" }));
    await waitFor(() => expect(getWiFiInsights).toHaveBeenLastCalledWith("gw-1", "1d"));
    expect(screen.getByRole("radio", { name: "1 day" }).getAttribute("aria-checked")).toBe("true");
  });

  it("explains that sampling has not produced data yet", async () => {
    getWiFiInsights.mockResolvedValue({
      ...sample, last_sample_at: null, total_bytes_down: 0, total_bytes_up: 0, bandwidth: null, client_counts: null,
      top_ssids: null, top_clients: null, vendors: null, os_inferred: null, clients: null,
    } as unknown as WiFiInsightsData);
    render(<WiFiInsights deviceId="gw-1" />);
    await waitFor(() => expect(screen.getByText(/No Wi-Fi samples yet/)).toBeTruthy());
    expect(screen.queryByRole("img", { name: /Bandwidth/ })).toBeNull();
  });

  it("shows the chart values as a table on request", async () => {
    getWiFiInsights.mockResolvedValue(sample);
    render(<WiFiInsights deviceId="gw-1" />);
    await waitFor(() => expect(screen.getByRole("img", { name: /Bandwidth/ })).toBeTruthy());
    fireEvent.click(screen.getAllByRole("button", { name: "Show as table" })[0]);
    const table = screen.getByRole("table", { name: "Bandwidth values" });
    expect(within(table).getByText("12 Mbps")).toBeTruthy();
  });

  it("points a mesh satellite to its gateway instead of loading", () => {
    render(<WiFiInsights deviceId="sat-1" satelliteOf="Lab FastMile" />);
    expect(screen.getByText(/reported by its gateway, Lab FastMile/)).toBeTruthy();
    expect(getWiFiInsights).not.toHaveBeenCalled();
  });

  it("shows a load failure without hiding the panel", async () => {
    getWiFiInsights.mockRejectedValue(new Error("boom"));
    render(<WiFiInsights deviceId="gw-1" />);
    await waitFor(() => expect(screen.getByText(/Could not load Wi-Fi insights: boom/)).toBeTruthy());
  });
});
