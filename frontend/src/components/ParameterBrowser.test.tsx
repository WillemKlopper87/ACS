import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ParameterBrowser } from "./ParameterBrowser";
import type { ParameterCache } from "../api/types";

const now = new Date().toISOString();
const old = new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString();
const params: ParameterCache = {
  "Device.DeviceInfo.SoftwareVersion": { value: "1.2501.00.0798", updated_at: old, source: "INFORM" },
  "Device.DeviceInfo.ProvisioningCode": { value: "", updated_at: old, source: "INFORM" },
  "Device.WiFi.SSID.1.SSID": { value: "Home", updated_at: now, source: "GET_PARAMETER_VALUES" },
  "Device.WiFi.SSID.10.SSID": { value: "Guest", updated_at: old, source: "GET_PARAMETER_VALUES" },
  "Device.WiFi.SSID.2.SSID": { value: "Office", updated_at: old, source: "GET_PARAMETER_VALUES" },
  "Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.RSRPCurrent": { value: "-32768", updated_at: old, source: "GET_PARAMETER_VALUES" },
};
const writable = { "Device.WiFi.SSID.1.SSID": true, "Device.DeviceInfo.ProvisioningCode": true, "Device.DeviceInfo.SoftwareVersion": false };

function setup(extra: Partial<Parameters<typeof ParameterBrowser>[0]> = {}) {
  const onSelect = vi.fn();
  render(<ParameterBrowser params={params} writable={writable} selectedPath={null} onSelect={onSelect} renderDetail={() => <div>history-panel</div>} {...extra} />);
  return onSelect;
}

describe("ParameterBrowser", () => {
  beforeEach(() => cleanup());

  it("starts with top-level branches collapsed and shows leaf counts", () => {
    setup();
    // Header total plus the Device. root, which is open by default so its
    // subtrees are visible collapsed.
    expect(screen.getAllByText("6 parameters").length).toBe(2);
    expect(screen.getByText(/▸ WiFi/)).toBeTruthy();
    expect(screen.getByText("3 parameters")).toBeTruthy(); // WiFi branch
    expect(screen.queryByText("Home")).toBeNull();
  });

  it("expands a branch and orders numeric instances numerically", () => {
    setup();
    fireEvent.click(screen.getByText(/▸ WiFi/));
    fireEvent.click(screen.getByText(/▸ SSID/));
    const instances = screen.getAllByText(/^▸ \d+$/).map((el) => el.textContent);
    expect(instances).toEqual(["▸ 1", "▸ 2", "▸ 10"]);
  });

  it("searches path and value as a flat list", () => {
    setup();
    fireEvent.change(screen.getByLabelText("Search parameters"), { target: { value: "office" } });
    expect(screen.getByText("1 of 6 parameters match")).toBeTruthy();
    expect(screen.getByText("Device.WiFi.SSID.2.SSID")).toBeTruthy();
  });

  it("filters to writable parameters from discovery", () => {
    setup();
    fireEvent.click(screen.getByLabelText("Writable"));
    expect(screen.getByText("2 of 6 parameters match")).toBeTruthy();
  });

  it("shows sentinel readings as no value and opens history on click", () => {
    const onSelect = setup({ selectedPath: "Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.RSRPCurrent" });
    fireEvent.change(screen.getByLabelText("Search parameters"), { target: { value: "RSRPCurrent" } });
    expect(screen.getByText("no value (-32768)")).toBeTruthy();
    expect(screen.getByText("history-panel")).toBeTruthy();
    fireEvent.click(screen.getByText("Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.RSRPCurrent"));
    expect(onSelect).toHaveBeenCalledWith("Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.RSRPCurrent");
  });
});
