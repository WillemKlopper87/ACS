import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { DeviceCompatibility } from "./DeviceCompatibility";

vi.mock("../api/client", () => ({
  ApiError: class extends Error {},
  api: {
    getStandardsAssessment: vi.fn().mockResolvedValue({ paths: [{ path: "Device.WiFi.SSID.{i}.Enable", classification: "standard" }], capabilities: { wifi: true } }),
    getConnectionDiagnostics: vi.fn().mockResolvedValue({ recommendations: [], checks: [] }),
    getCompatibility: vi.fn().mockResolvedValue({ connected: true, discovered: true, read_tested: true, write_tested: false, firmware_qualified: false }),
    getTelemetryHealth: vi.fn().mockResolvedValue({ receivers: [{ receiver_id: "receiver", state: "missing_reports", alert: "No recent report" }] }),
  },
}));

describe("Device compatibility evidence", () => {
  beforeEach(() => cleanup());
  it("shows distinct stages and a missing-report alert without implying firmware qualification", async () => {
    render(<DeviceCompatibility id="device" writable={false} />);
    fireEvent.click(screen.getByText("Refresh evidence"));
    await waitFor(() => expect(screen.getByText("Firmware qualified")).toBeTruthy());
    expect(screen.getAllByText("Not established").length).toBe(2);
    expect(screen.getByText(/No recent report/)).toBeTruthy();
    expect((screen.getByText("Renew collection credentials") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByText("Find existing instances") as HTMLButtonElement).disabled).toBe(true);
  });
});
