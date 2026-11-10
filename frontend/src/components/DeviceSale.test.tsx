import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { Onboarding } from "../api/client";
import type { Device } from "../api/types";
import { DeviceSale } from "./DeviceSale";

const sale: Onboarding = {
  id: "ob-1",
  expected_serial: "CSC25430018F",
  device_id: "dev-1",
  customer_id: "cust-a",
  state: "IN_SERVICE",
  state_reason: "",
  role: "gateway",
  commercial_model: "retail",
  source: "console",
  config_jobs: [],
  created_at: "2026-10-30T10:00:00Z",
  updated_at: "2026-10-30T10:05:00Z",
};
const device = { id: "dev-1", oui_serial: "A4FC77+3TG02241+CSC25430018F", serial_number: "CSC25430018F" } as Device;

const api = vi.hoisted(() => ({
  listOnboardings: vi.fn(),
  listSites: vi.fn(),
  listServicePlans: vi.fn(),
  listTemplates: vi.fn(),
  listCustomers: vi.fn(),
}));
vi.mock("../api/client", () => ({ api, ApiError: class extends Error {} }));
vi.mock("../auth/useAuth", () => ({ useAuth: () => ({ role: "manager" }) }));

function show(items: Onboarding[]) {
  api.listOnboardings.mockResolvedValue({ items });
  api.listSites.mockResolvedValue({ items: [] });
  api.listServicePlans.mockResolvedValue({ items: [] });
  api.listTemplates.mockResolvedValue({ items: [] });
  api.listCustomers.mockResolvedValue({ items: [] });
  return render(
    <MemoryRouter>
      <DeviceSale device={device} />
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("DeviceSale", () => {
  it("offers to claim a device whose sale has ended", async () => {
    show([{ ...sale, state: "ENDED", state_reason: "service ended: device returned" }]);
    expect(await screen.findByText(/back in the pool/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Claim device" })).toBeTruthy();
  });

  it("does not offer a claim while the sale is in service", async () => {
    show([sale]);
    await waitFor(() => expect(api.listOnboardings).toHaveBeenCalled());
    expect(await screen.findByText("Open in Onboarding")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Claim device" })).toBeNull();
  });

  it("offers the claim when no sale exists at all", async () => {
    show([]);
    expect(await screen.findByRole("button", { name: "Claim device" })).toBeTruthy();
  });
});
