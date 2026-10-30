import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { Onboarding as OnboardingRecord } from "../api/client";
import { Onboarding } from "./Onboarding";

const held: OnboardingRecord = {
  id: "ob-held",
  expected_serial: "CSC25430018F",
  expected_iccid: "8927…1234",
  device_id: "dev-1",
  customer_id: "cust-a",
  state: "ON_HOLD",
  state_reason: "SIM 8927…9999 does not match the sale (8927…1234)",
  role: "gateway",
  commercial_model: "retail",
  source: "console",
  config_jobs: [],
  created_at: "2026-10-30T10:00:00Z",
  updated_at: "2026-10-30T10:05:00Z",
  contacted_at: "2026-10-30T10:05:00Z",
};
const planned: OnboardingRecord = { ...held, id: "ob-planned", expected_serial: "PLAN0001", device_id: null, state: "PLANNED", state_reason: "", contacted_at: null };

const role = vi.hoisted(() => ({ value: "manager" }));
const api = vi.hoisted(() => ({
  listOnboardings: vi.fn(),
  listUnclaimedDevices: vi.fn(),
  listSites: vi.fn(),
  listServicePlans: vi.fn(),
  listTemplates: vi.fn(),
  listCustomers: vi.fn(),
  retryOnboarding: vi.fn(),
  cancelOnboarding: vi.fn(),
  createOnboarding: vi.fn(),
  createSite: vi.fn(),
  createServicePlan: vi.fn(),
}));
vi.mock("../api/client", () => ({ api, ApiError: class extends Error {} }));
vi.mock("../auth/useAuth", () => ({ useAuth: () => ({ role: role.value }) }));

function setup(path: string) {
  api.listOnboardings.mockResolvedValue({ items: [held, planned] });
  api.listUnclaimedDevices.mockResolvedValue({
    items: [{ id: "dev-stray", oui_serial: "A4FC77+3TG02241+STRAY1", serial_number: "STRAY1", manufacturer: "Nokia", product_class: "3TG02241", online_status: "ONLINE" }],
  });
  api.listSites.mockResolvedValue({ items: [{ id: "site-a", name: "14 Oak Ave", customer_id: "cust-a", created_at: "x" }] });
  api.listServicePlans.mockResolvedValue({
    items: [{ id: "plan-1", code: "HOME-5G", name: "Home 5G", customer_id: null, template_ids: [], cellular: { apn: "internet" }, allowed_plmns: ["65506"], require_discovery: true, created_at: "x" }],
  });
  api.listTemplates.mockResolvedValue({ items: [] });
  api.listCustomers.mockResolvedValue({ items: [{ id: "cust-a", name: "Acme ISP" }] });
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Onboarding />
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  role.value = "manager";
});

describe("Onboarding screen", () => {
  it("lists what needs attention, with counts per tab, and explains the hold", async () => {
    setup("/onboarding/attention");
    expect(await screen.findByText("CSC25430018F")).toBeTruthy();
    expect(screen.queryByText("PLAN0001")).toBeNull();
    const nav = screen.getByRole("navigation", { name: "Onboarding" });
    expect(within(nav).getByText("Needs attention (1)")).toBeTruthy();
    expect(within(nav).getByText("Planned (1)")).toBeTruthy();
    expect(within(nav).getByText("Unclaimed (1)")).toBeTruthy();

    fireEvent.click(screen.getByText("CSC25430018F"));
    const detail = await screen.findByLabelText("Onboarding of CSC25430018F");
    expect(within(detail).getByText(/stopped here/)).toBeTruthy();
    expect(within(detail).getByText("8927…1234")).toBeTruthy();

    api.retryOnboarding.mockResolvedValue({ ...held, state: "CONTACTED" });
    fireEvent.click(within(detail).getByText("Retry checks"));
    await waitFor(() => expect(api.retryOnboarding).toHaveBeenCalledWith("ob-held"));
  });

  it("claims an unclaimed CPE through the wizard, creating its new site for the chosen customer", async () => {
    setup("/onboarding/unclaimed");
    fireEvent.click(await screen.findByText("Claim"));
    const dialog = screen.getByRole("dialog", { name: "Claim device" });
    expect(within(dialog).getByText("STRAY1")).toBeTruthy();

    fireEvent.click(within(dialog).getByText("Next")); // Device
    await waitFor(() => expect(within(dialog).getByText("Acme ISP")).toBeTruthy());
    fireEvent.change(within(dialog).getByLabelText("Customer (ISP tenant)"), { target: { value: "cust-a" } });
    fireEvent.change(within(dialog).getByLabelText("BSS account reference"), { target: { value: "ACC-77" } });
    fireEvent.click(within(dialog).getByText("Next")); // Who

    fireEvent.click(within(dialog).getByLabelText("New site"));
    expect((within(dialog).getByText("Next") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText("Site name"), { target: { value: "9 Elm St" } });
    fireEvent.click(within(dialog).getByText("Next")); // Where

    fireEvent.change(within(dialog).getByLabelText("Service plan"), { target: { value: "plan-1" } });
    expect(within(dialog).getByText("APN internet")).toBeTruthy();
    fireEvent.click(within(dialog).getByText("Next")); // What

    api.createSite.mockResolvedValue({ id: "site-new" });
    api.createOnboarding.mockResolvedValue({ ...planned, id: "ob-new", expected_serial: "STRAY1" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Claim device" }));
    await waitFor(() => expect(api.createOnboarding).toHaveBeenCalled());
    expect(api.createSite).toHaveBeenCalledWith(expect.objectContaining({ name: "9 Elm St", customer_id: "cust-a" }));
    expect(api.createOnboarding).toHaveBeenCalledWith(
      expect.objectContaining({ device_id: "dev-stray", customer_id: "cust-a", account_ref: "ACC-77", site_id: "site-new", service_plan_id: "plan-1" }),
    );
  });

  it("refuses an allowed network that is not MCC+MNC before it reaches the API", async () => {
    setup("/onboarding/plans");
    fireEvent.change(await screen.findByLabelText("Code"), { target: { value: "P1" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Plan 1" } });
    fireEvent.change(screen.getByLabelText("Allowed networks (MCC+MNC)"), { target: { value: "65506 MTN" } });
    expect(screen.getByText(/Not MCC\+MNC.*MTN/)).toBeTruthy();
    expect((screen.getByText("Create plan") as HTMLButtonElement).disabled).toBe(true);
  });

  it("leaves changes to managers", async () => {
    role.value = "noc";
    setup("/onboarding/attention");
    expect(((await screen.findByText("New sale")) as HTMLButtonElement).disabled).toBe(true);
  });
});
