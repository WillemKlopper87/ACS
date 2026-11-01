import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { DeviceXMPPState, XMPPProvisioning } from "../api/client";
import { XmppPanel } from "./XmppPanel";

const { getDeviceXMPP, provisionXMPP, ApiError } = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  }
  return { getDeviceXMPP: vi.fn(), provisionXMPP: vi.fn(), ApiError };
});
vi.mock("../api/client", () => ({ ApiError, api: { getDeviceXMPP, provisionXMPP } }));

const acsUp = { configured: true, connected: true, jid: "acs@xmpp.acs.lab/acs", cpe_server_address: "203.0.113.10", cpe_server_port: 5222 };
const cpeReady = { supported_conn_req_methods: "XMPP,HTTP", conn_req_jabber_id: "", conn_req_jabber_id_valid: false, connection_request_mode: "PERIODIC_FALLBACK_ONLY" };

function run(overrides: Partial<XMPPProvisioning>): XMPPProvisioning {
  return {
    state: "CONFIGURE", attempts: 1, domain: "xmpp.acs.lab", resource: "cwmp", server_address: "203.0.113.10", server_port: 5222,
    keepalive_interval: 60, acs_jid: "acs@xmpp.acs.lab", relax_certificate_verification: true, bypass_hostname_check: true,
    requested_by: "noc-1", created_at: "2026-10-30T15:00:00Z", updated_at: "2026-10-30T15:03:00Z", ...overrides,
  };
}

const state = (overrides: Partial<DeviceXMPPState>): DeviceXMPPState => ({ device_id: "gw-1", acs: acsUp, cpe: cpeReady, provisioning: null, ...overrides });

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("XmppPanel", () => {
  it("renders nothing for a device without XMPP support", async () => {
    getDeviceXMPP.mockResolvedValue(state({ cpe: { ...cpeReady, supported_conn_req_methods: "HTTP" } }));
    const { container } = render(<XmppPanel deviceId="sat-1" writable />);
    await waitFor(() => expect(getDeviceXMPP).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });

  it("explains why setup is unavailable while the ACS has no XMPP server", async () => {
    getDeviceXMPP.mockResolvedValue(state({ acs: { configured: false } }));
    render(<XmppPanel deviceId="gw-1" writable />);
    expect(await screen.findByText(/not enabled on this ACS/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Set up…" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("starts provisioning with the certificate relaxations only when ticked, and warns about the plain-HTTP password", async () => {
    getDeviceXMPP
      .mockResolvedValueOnce(state({}))
      .mockResolvedValue(state({ provisioning: run({ state: "ADD_CONNECTION", attempts: 0 }) }));
    provisionXMPP.mockResolvedValue({ existing: false, provisioning: run({ state: "ADD_CONNECTION", attempts: 0 }) });
    render(<XmppPanel deviceId="gw-1" writable />);
    fireEvent.click(await screen.findByRole("button", { name: "Set up…" }));

    const dialog = screen.getByRole("dialog", { name: "Set up XMPP Connection Request" });
    expect(dialog.textContent).toMatch(/unencrypted/);
    const relax = screen.getByLabelText(/Don't verify the server certificate/) as HTMLInputElement;
    const bypass = screen.getByLabelText(/Skip the hostname check/) as HTMLInputElement;
    expect(relax.checked).toBe(false);
    expect(bypass.checked).toBe(false);
    fireEvent.click(relax);
    fireEvent.click(screen.getByRole("button", { name: "Start provisioning" }));

    await waitFor(() =>
      expect(provisionXMPP).toHaveBeenCalledWith("gw-1", { relax_certificate_verification: true, bypass_hostname_check: false, restart: false }),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    const steps = screen.getByRole("list", { name: "Provisioning steps" });
    expect(within(steps).getByText("Create the XMPP connection").closest("li")?.getAttribute("aria-current")).toBe("step");
  });

  it("shows progress and re-reads it while a run is in progress", async () => {
    getDeviceXMPP.mockResolvedValue(state({ provisioning: run({ state: "VERIFY", attempts: 2, connection_status: "Connecting" }) }));
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<XmppPanel deviceId="gw-1" writable />);
    // Generous timeout: fake timers are active and a full parallel run is slow.
    const steps = await screen.findByRole("list", { name: "Provisioning steps" }, { timeout: 5000 });
    expect(within(steps).getByText("Wait for the device to log in").closest("li")?.getAttribute("aria-current")).toBe("step");
    expect(screen.getByText(/attempt 2/)).toBeTruthy();
    expect(screen.getByText("Connecting")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Set up…" })).toBeNull();
    // The poll interval starts in an effect after that render; let it run first.
    await act(async () => {});
    await act(async () => {
      vi.advanceTimersByTime(15_000);
    });
    await waitFor(() => expect(getDeviceXMPP).toHaveBeenCalledTimes(2));
  });

  it("shows a failed run's error and retries with a fresh credential", async () => {
    getDeviceXMPP.mockResolvedValue(state({ provisioning: run({ state: "FAILED", last_error: "CPE fault 9005 on Device.XMPP.Connection.1.Server." }) }));
    provisionXMPP.mockResolvedValue({ existing: false, provisioning: run({ state: "DISCOVER_CONNECTION" }) });
    render(<XmppPanel deviceId="gw-1" writable />);
    expect(await screen.findByText(/CPE fault 9005/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry…" }));
    fireEvent.click(screen.getByRole("button", { name: "Start provisioning" }));
    await waitFor(() => expect(provisionXMPP).toHaveBeenCalledWith("gw-1", expect.objectContaining({ restart: true })));
  });

  it("shows the device's Jabber ID once done and offers re-provisioning", async () => {
    getDeviceXMPP.mockResolvedValue(
      state({
        cpe: { ...cpeReady, conn_req_jabber_id: "xmpp-ab12@xmpp.acs.lab/cwmp", conn_req_jabber_id_valid: true, connection_request_mode: "XMPP", xmpp_connection_status: "Enabled" },
        provisioning: run({ state: "DONE", jabber_id: "xmpp-ab12@xmpp.acs.lab/cwmp", completed_at: "2026-10-30T15:08:00Z" }),
      }),
    );
    render(<XmppPanel deviceId="gw-1" writable />);
    expect(await screen.findByText("xmpp-ab12@xmpp.acs.lab/cwmp")).toBeTruthy();
    expect(screen.getByText(/Connection Requests go over XMPP/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Re-provision…" })).toBeTruthy();
  });

  it("keeps the dialog open with the server's reason when provisioning is refused", async () => {
    getDeviceXMPP.mockResolvedValue(state({}));
    provisionXMPP.mockRejectedValue(new ApiError(409, "parameter discovery has not found a writable Device.XMPP.Connection. table on this device; run parameter discovery first"));
    render(<XmppPanel deviceId="gw-1" writable />);
    fireEvent.click(await screen.findByRole("button", { name: "Set up…" }));
    fireEvent.click(screen.getByRole("button", { name: "Start provisioning" }));
    expect(await screen.findByText(/run parameter discovery first/)).toBeTruthy();
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("disables setup for an operator who cannot write", async () => {
    getDeviceXMPP.mockResolvedValue(state({}));
    render(<XmppPanel deviceId="gw-1" writable={false} />);
    expect((await screen.findByRole("button", { name: "Set up…" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
