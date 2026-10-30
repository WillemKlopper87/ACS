import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { SIMView } from "../api/client";
import { SimPanel } from "./SimPanel";

const { getSIM, revealSIM, getParametersLive, ApiError } = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  }
  return { getSIM: vi.fn(), revealSIM: vi.fn(), getParametersLive: vi.fn(), ApiError };
});
vi.mock("../api/client", () => ({ ApiError, api: { getSIM, revealSIM, getParametersLive } }));

const field = (kind: SIMView["identifiers"][number]["kind"], path: string, value: string, masked: boolean) => ({
  kind, path, value, masked, updated_at: "2026-10-30T12:00:00Z", source: "GET_PARAMETER_VALUES",
});

const masked: SIMView = {
  device_id: "gw-1",
  supported: true,
  revealed: false,
  identifiers: [
    field("iccid", "Device.Cellular.Interface.1.USIM.ICCID", "89••••••••••••••1234", true),
    field("imsi", "Device.Cellular.Interface.1.USIM.IMSI", "65501••••••5678", true),
  ],
  context: [field("context", "Device.Cellular.Interface.1.USIM.Status", "Valid", false)],
  refresh_paths: [],
};

const revealed: SIMView = {
  ...masked,
  revealed: true,
  identifiers: [
    field("iccid", "Device.Cellular.Interface.1.USIM.ICCID", "89270000000000001234", false),
    field("imsi", "Device.Cellular.Interface.1.USIM.IMSI", "655010000005678", false),
  ],
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("SimPanel", () => {
  it("shows identifiers masked with their kind and the SIM context", async () => {
    getSIM.mockResolvedValue(masked);
    render(<SimPanel deviceId="gw-1" role="manager" writable />);
    await waitFor(() => expect(screen.getByText("65501••••••5678")).toBeTruthy());
    expect(screen.getByText("IMSI")).toBeTruthy();
    expect(screen.getByText("ICCID")).toBeTruthy();
    expect(screen.getByText("Valid")).toBeTruthy();
    // Only a superadmin can ask for full values.
    expect(screen.queryByRole("button", { name: /Reveal/ })).toBeNull();
  });

  it("renders nothing for a device without a SIM", async () => {
    getSIM.mockResolvedValue({ ...masked, supported: false, identifiers: [], context: [], reason: "no Device.Cellular." });
    const { container } = render(<SimPanel deviceId="sat-1" role="superadmin" writable />);
    await waitFor(() => expect(getSIM).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });

  it("queues a read of identifiers not yet in the cache", async () => {
    getSIM.mockResolvedValue({ ...masked, identifiers: [], refresh_paths: ["Device.Cellular.Interface.1.USIM.IMSI"] });
    getParametersLive.mockResolvedValue({ command_key: "getparam_1", status: "QUEUED" });
    render(<SimPanel deviceId="gw-1" role="noc" writable />);
    fireEvent.click(await screen.findByRole("button", { name: "Read SIM values" }));
    await waitFor(() => expect(getParametersLive).toHaveBeenCalledWith("gw-1", ["Device.Cellular.Interface.1.USIM.IMSI"]));
    expect(await screen.findByText(/getparam_1/)).toBeTruthy();
  });

  it("reveals full values to a superadmin only with a reason, then hides them again", async () => {
    getSIM.mockResolvedValue(masked);
    revealSIM.mockResolvedValue(revealed);
    render(<SimPanel deviceId="gw-1" role="superadmin" writable />);
    fireEvent.click(await screen.findByRole("button", { name: "Reveal full values…" }));

    const dialog = screen.getByRole("dialog", { name: "Reveal subscriber identifiers" });
    expect(dialog.textContent).toMatch(/audit log/);
    const confirm = screen.getByRole("button", { name: "Reveal and record" }) as HTMLButtonElement;
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "too short" } });
    expect(confirm.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "RICA verification for ticket 4471" } });
    fireEvent.change(screen.getByLabelText(/Reference/), { target: { value: "INC-4471" } });
    expect(confirm.disabled).toBe(false);
    fireEvent.click(confirm);

    await waitFor(() => expect(screen.getByText("655010000005678")).toBeTruthy());
    expect(revealSIM).toHaveBeenCalledWith("gw-1", "RICA verification for ticket 4471", "INC-4471");
    expect(screen.queryByRole("dialog")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Hide" }));
    expect(screen.queryByText("655010000005678")).toBeNull();
    expect(screen.getByText("65501••••••5678")).toBeTruthy();
  });

  it("hides revealed values automatically after two minutes", async () => {
    getSIM.mockResolvedValue(masked);
    revealSIM.mockResolvedValue(revealed);
    render(<SimPanel deviceId="gw-1" role="superadmin" writable />);
    fireEvent.click(await screen.findByRole("button", { name: "Reveal full values…" }));
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "RICA verification for ticket 4471" } });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    fireEvent.click(screen.getByRole("button", { name: "Reveal and record" }));
    // Generous timeout: under a full parallel run the reveal can take longer
    // than waitFor's 1 s default to render.
    await screen.findByText("655010000005678", {}, { timeout: 5000 });
    // The hide timer starts in an effect after that render; let React run
    // it before moving the clock, or the advance can happen first.
    await act(async () => {});
    await act(async () => {
      vi.advanceTimersByTime(119_000);
    });
    expect(screen.getByText("655010000005678")).toBeTruthy();
    await act(async () => {
      vi.advanceTimersByTime(1_000);
    });
    expect(screen.queryByText("655010000005678")).toBeNull();
  });

  it("closes only the dialog on Escape, not the device panel behind it", async () => {
    getSIM.mockResolvedValue(masked);
    const pageEscape = vi.fn();
    window.addEventListener("keydown", pageEscape);
    try {
      render(<SimPanel deviceId="gw-1" role="superadmin" writable />);
      fireEvent.click(await screen.findByRole("button", { name: "Reveal full values…" }));
      fireEvent.keyDown(screen.getByLabelText(/Reason/), { key: "Escape" });
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(pageEscape).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", pageEscape);
    }
  });

  it("keeps the dialog open and shows why a reveal was refused", async () => {
    getSIM.mockResolvedValue(masked);
    revealSIM.mockRejectedValue(new ApiError(403, "subscriber identity disclosure is restricted to superadmin operators"));
    render(<SimPanel deviceId="gw-1" role="superadmin" writable />);
    fireEvent.click(await screen.findByRole("button", { name: "Reveal full values…" }));
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "RICA verification for ticket 4471" } });
    fireEvent.click(screen.getByRole("button", { name: "Reveal and record" }));
    expect(await screen.findByText(/restricted to superadmin operators/)).toBeTruthy();
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(screen.queryByText("655010000005678")).toBeNull();
  });
});
