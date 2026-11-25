import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ReadParametersPanel } from "./ReadParametersPanel";

const { getParametersLive, ApiError, toast } = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  }
  return { getParametersLive: vi.fn(), ApiError, toast: vi.fn() };
});
vi.mock("../api/client", () => ({ ApiError, api: { getParametersLive } }));
vi.mock("../lib/toast", () => ({ toast }));

const LAT = "Device.DeviceInfo.Location.1.X_ALU-COM_Latitude";
const LON = "Device.DeviceInfo.Location.1.X_ALU-COM_Longitude";
const ALT = "Device.DeviceInfo.Location.1.X_ALU-COM_Altitude";
const names: Record<string, boolean> = {
  "Device.DeviceInfo.UpTime": false,
  "Device.Cellular.RoamingStatus": false,
  "Device.Cellular.Interface.1.NetworkInUse": false,
  "Device.DeviceInfo.Location.1.Source": false,
  [LAT]: false,
  [LON]: false,
  [ALT]: false,
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const box = () => screen.getByLabelText("Paths to read, one per line") as HTMLTextAreaElement;
const readBtn = () => screen.getByRole("button", { name: "Read now" }) as HTMLButtonElement;
const presetBtn = () => screen.getByRole("button", { name: "Use FastMile telemetry preset" }) as HTMLButtonElement;
const coords = () => screen.getByLabelText("Include the location coordinates (read once, to learn their format)") as HTMLInputElement;
const type = (text: string) => fireEvent.change(box(), { target: { value: text } });

describe("ReadParametersPanel", () => {
  it("fills the textarea with the preset and omits the coordinates", () => {
    render(<ReadParametersPanel deviceId="gw-1" names={names} writable />);
    fireEvent.click(presetBtn());
    expect(box().value).toContain("Device.DeviceInfo.UpTime");
    expect(box().value).not.toContain("_Latitude");
    expect(coords().checked).toBe(false);
  });

  it("adds the three coordinate paths only when the box is ticked", () => {
    render(<ReadParametersPanel deviceId="gw-1" names={names} writable />);
    fireEvent.click(coords());
    fireEvent.click(presetBtn());
    for (const p of [LAT, LON, ALT]) expect(box().value).toContain(p);
  });

  it("sends the trimmed, de-duplicated paths once, even on a double click", async () => {
    getParametersLive.mockResolvedValue({ command_key: "getparam_9", status: "QUEUED" });
    render(<ReadParametersPanel deviceId="gw-1" names={names} writable />);
    fireEvent.click(coords());
    type("  Device.DeviceInfo.UpTime \n\nDevice.DeviceInfo.UpTime\n" + LAT + "\n");
    fireEvent.click(readBtn());
    fireEvent.click(readBtn());
    await waitFor(() => expect(toast).toHaveBeenCalledWith("Queued getparam_9. It runs when the device next checks in."));
    expect(getParametersLive).toHaveBeenCalledTimes(1);
    expect(getParametersLive).toHaveBeenCalledWith("gw-1", ["Device.DeviceInfo.UpTime", LAT]);
    expect(coords().checked).toBe(false);
  });

  it("blocks a path the device does not have and names it", () => {
    render(<ReadParametersPanel deviceId="gw-1" names={names} writable />);
    type("Device.Nope.X");
    expect(readBtn().disabled).toBe(true);
    expect(document.body.textContent).toContain("Device.Nope.X");
  });

  it("blocks an object path ending in a dot", () => {
    render(<ReadParametersPanel deviceId="gw-1" names={names} writable />);
    type("Device.DeviceInfo.");
    expect(readBtn().disabled).toBe(true);
    expect(document.body.textContent).toMatch(/object path/i);
    expect(document.body.textContent).toContain("Device.DeviceInfo.");
  });

  it("blocks more than 60 paths", () => {
    const many: Record<string, boolean> = {};
    const lines: string[] = [];
    for (let i = 0; i < 61; i++) {
      many[`Device.X.${i}`] = false;
      lines.push(`Device.X.${i}`);
    }
    render(<ReadParametersPanel deviceId="gw-1" names={many} writable />);
    type(lines.join("\n"));
    expect(readBtn().disabled).toBe(true);
    expect(document.body.textContent).toMatch(/at most 60/);
  });

  it("is disabled for a read-only role and says why", () => {
    render(<ReadParametersPanel deviceId="gw-1" names={names} writable={false} />);
    type("Device.DeviceInfo.UpTime");
    expect(readBtn().disabled).toBe(true);
    expect(document.body.textContent).toMatch(/your role cannot/i);
    fireEvent.click(readBtn());
    expect(getParametersLive).not.toHaveBeenCalled();
  });

  it("says the model has not been discovered when names is null", async () => {
    getParametersLive.mockResolvedValue({ command_key: "getparam_1", status: "QUEUED" });
    render(<ReadParametersPanel deviceId="gw-1" names={null} writable />);
    expect(presetBtn().disabled).toBe(true);
    expect(document.body.textContent).toMatch(/not been discovered/i);
    type("Device.Anything.Typed");
    expect(readBtn().disabled).toBe(false);
    fireEvent.click(readBtn());
    await waitFor(() => expect(getParametersLive).toHaveBeenCalledWith("gw-1", ["Device.Anything.Typed"]));
  });

  it("shows the API's message when the read cannot be queued", async () => {
    getParametersLive.mockRejectedValue(new ApiError(409, "device is busy"));
    render(<ReadParametersPanel deviceId="gw-1" names={names} writable />);
    type("Device.DeviceInfo.UpTime");
    fireEvent.click(readBtn());
    await waitFor(() => expect(toast).toHaveBeenCalledWith("device is busy", "error"));
  });
});
