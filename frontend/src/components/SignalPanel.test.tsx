import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { SignalHistory } from "../api/types";
import { SignalPanel } from "./SignalPanel";

const reply: SignalHistory = {
  from: "2026-11-18T08:00:00Z",
  to: "2026-11-25T08:00:00Z",
  bucket_seconds: 0,
  interval_seconds: 900,
  samples: [
    { at: "2026-11-24T10:00:00Z", rsrp: -105, rsrq: -12, snr: null, rssi: null, band: "B3", rat: "LTE" },
    { at: "2026-11-24T10:15:00Z", rsrp: -98, rsrq: -10, snr: null, rssi: null, band: "B3", rat: "LTE" },
    { at: "2026-11-24T10:30:00Z", rsrp: -101, rsrq: null, snr: null, rssi: null, band: "n78", rat: "NR" },
  ],
};

const getSignalHistory = vi.fn();
vi.mock("../api/client", () => ({ api: { getSignalHistory: (...a: unknown[]) => getSignalHistory(...a) } }));

afterEach(() => {
  cleanup();
  getSignalHistory.mockReset();
});

describe("SignalPanel", () => {
  it("offers the metrics and ranges and asks for 7 days first", async () => {
    getSignalHistory.mockResolvedValue(reply);
    render(<SignalPanel deviceId="cpe-1" />);
    for (const n of ["RSRP", "RSRQ", "SNR", "RSSI", "24 hours", "7 days", "30 days"]) {
      expect(screen.getByRole("button", { name: n })).toBeTruthy();
    }
    expect(screen.getByRole("heading", { name: /Signal/ })).toBeTruthy();
    await waitFor(() => expect(getSignalHistory).toHaveBeenCalledWith("cpe-1", 168));
  });

  it("asks for 720 hours for 30 days", async () => {
    getSignalHistory.mockResolvedValue(reply);
    render(<SignalPanel deviceId="cpe-1" />);
    await screen.findByRole("img", { name: /RSRP/ });
    fireEvent.click(screen.getByRole("button", { name: "30 days" }));
    await waitFor(() => expect(getSignalHistory).toHaveBeenLastCalledWith("cpe-1", 720));
  });

  it("draws a chart labelled with the metric and the count, with min and max", async () => {
    getSignalHistory.mockResolvedValue(reply);
    render(<SignalPanel deviceId="cpe-1" />);
    expect(await screen.findByRole("img", { name: /RSRP.*3 readings/ })).toBeTruthy();
    expect(screen.getByText(/-105 dBm/)).toBeTruthy();
    expect(screen.getByText(/-98 dBm/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "RSRQ" }));
    expect(await screen.findByRole("img", { name: /RSRQ.*2 readings/ })).toBeTruthy();
  });

  it("lists the rows with their times in a table", async () => {
    getSignalHistory.mockResolvedValue(reply);
    render(<SignalPanel deviceId="cpe-1" />);
    await screen.findByRole("img", { name: /RSRP/ });
    fireEvent.click(screen.getByRole("button", { name: "Show as table" }));
    expect(screen.getByText("2026-11-24 10:15:00")).toBeTruthy();
    expect(screen.getByText("n78")).toBeTruthy();
  });

  it("says so when there are no samples, with the sample interval note", async () => {
    getSignalHistory.mockResolvedValue({ ...reply, samples: [] });
    render(<SignalPanel deviceId="cpe-1" />);
    expect(await screen.findByText(/No signal samples yet/)).toBeTruthy();
    expect(screen.getByText(/sample interval/i)).toBeTruthy();
  });

  it("copes with a reply that has no samples key", async () => {
    getSignalHistory.mockResolvedValue({ items: [] });
    render(<SignalPanel deviceId="cpe-1" />);
    expect(await screen.findByText(/No signal samples yet/)).toBeTruthy();
  });

  it("says so when the metric has no readings", async () => {
    getSignalHistory.mockResolvedValue(reply);
    render(<SignalPanel deviceId="cpe-1" />);
    await screen.findByRole("img", { name: /RSRP/ });
    fireEvent.click(screen.getByRole("button", { name: "SNR" }));
    expect(await screen.findByText("No readings for this metric on this device.")).toBeTruthy();
  });

  it("shows an error when the request fails", async () => {
    getSignalHistory.mockRejectedValue(new Error("boom"));
    render(<SignalPanel deviceId="cpe-1" />);
    expect(await screen.findByText("Could not load the signal history.")).toBeTruthy();
  });

  it("states the resolution rule", async () => {
    getSignalHistory.mockResolvedValue(reply);
    render(<SignalPanel deviceId="cpe-1" />);
    expect(await screen.findByText(/larger of the sample interval and the CPE's check-in interval/)).toBeTruthy();
  });
});
