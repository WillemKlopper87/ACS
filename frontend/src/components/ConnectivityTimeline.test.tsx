import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { DeviceConnectivity } from "../api/types";
import { ConnectivityTimeline, barX } from "./ConnectivityTimeline";

const sample: DeviceConnectivity = {
  from: "2026-11-16T08:00:00Z",
  to: "2026-11-23T08:00:00Z",
  checkins: 300,
  interval_seconds: 900,
  threshold_seconds: 2250,
  availability_percent: 97.5,
  outages: [
    { last_seen: "2026-11-17T10:00:00Z", back_at: "2026-11-17T12:00:00Z", seconds: 7200, kind: "connectivity" },
    { last_seen: "2026-11-20T01:00:00Z", back_at: "2026-11-20T02:00:00Z", seconds: 3600, kind: "restart" },
    { last_seen: "2026-11-23T05:00:00Z", back_at: null, seconds: 10800, kind: "ongoing" },
  ],
};

const getDeviceConnectivity = vi.fn();
vi.mock("../api/client", () => ({ api: { getDeviceConnectivity: (...a: unknown[]) => getDeviceConnectivity(...a) } }));

afterEach(() => {
  cleanup();
  getDeviceConnectivity.mockReset();
});

describe("barX", () => {
  const from = Date.parse("2026-11-16T00:00:00Z");
  const to = Date.parse("2026-11-18T00:00:00Z");
  it("places a time proportionally and clamps to the window", () => {
    expect(barX("2026-11-17T00:00:00Z", from, to)).toBe(500);
    expect(barX("2026-11-10T00:00:00Z", from, to)).toBe(0);
    expect(barX(null, from, to)).toBe(1000); // ongoing runs to now
  });
});

describe("ConnectivityTimeline", () => {
  it("shows availability, each outage and what the device said", async () => {
    getDeviceConnectivity.mockResolvedValue(sample);
    render(<ConnectivityTimeline deviceId="cpe-1" />);
    expect(await screen.findByText("97.50%")).toBeTruthy();
    expect(getDeviceConnectivity).toHaveBeenCalledWith("cpe-1", 168);
    expect(screen.getByRole("img", { name: /3 outages/ })).toBeTruthy();
    // The kind is written out, not left to colour.
    expect(screen.getAllByText("Lost contact, CPE stayed up").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Restarted").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Silent now").length).toBeGreaterThan(0);
    expect(screen.getByText(/longest up to 3h 00m/)).toBeTruthy();
  });

  it("asks for the window the user picks", async () => {
    getDeviceConnectivity.mockResolvedValue(sample);
    render(<ConnectivityTimeline deviceId="cpe-1" />);
    await screen.findByText("97.50%");
    fireEvent.click(screen.getByRole("button", { name: "30 days" }));
    await waitFor(() => expect(getDeviceConnectivity).toHaveBeenLastCalledWith("cpe-1", 720));
  });

  it("says so when the device has no check-ins", async () => {
    getDeviceConnectivity.mockResolvedValue({ ...sample, checkins: 0, outages: [], availability_percent: null });
    render(<ConnectivityTimeline deviceId="cpe-1" />);
    expect(await screen.findByText(/No check-ins recorded in this window/)).toBeTruthy();
  });

  it("says so when the request fails", async () => {
    getDeviceConnectivity.mockRejectedValue(new Error("boom"));
    render(<ConnectivityTimeline deviceId="cpe-1" />);
    expect(await screen.findByText("Could not load the availability history.")).toBeTruthy();
  });
});
