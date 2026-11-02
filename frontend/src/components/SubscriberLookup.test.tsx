import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { api } from "../api/client";
import { SubscriberLookup } from "./SubscriberLookup";

vi.mock("../api/client", () => ({
  ApiError: class extends Error {},
  api: {
    lookupSubscribers: vi.fn().mockResolvedValue({
      count: 2,
      items: [
        { device_id: "d1", account_id: "ACC-1", subscriber_name: "Thandi Nkosi", vip: true, oui_serial: "001122-FM-1234A1B2", manufacturer: "Nokia", model: "FastMile",
          site: { street: "1 Main Rd", suburb: "Sandton" }, display_name: "x", matched: ["name"] },
        { device_id: "d2", account_id: "ACC-2", subscriber_name: "Thandi Nkosi", vip: false, oui_serial: "001122-FM-9999C3D4", site: { suburb: "Soweto" },
          display_name: "y", matched: ["name"] },
      ],
    }),
  },
}));

describe("Subscriber lookup", () => {
  beforeEach(() => cleanup());
  it("searches on submit only and tells namesakes apart by address, account and serial digits", async () => {
    render(
      <MemoryRouter>
        <SubscriberLookup />
      </MemoryRouter>,
    );
    const box = screen.getByLabelText("Find a subscriber");
    fireEvent.change(box, { target: { value: "thandi" } });
    expect(api.lookupSubscribers).not.toHaveBeenCalled();
    fireEvent.submit(box.closest("form")!);
    await waitFor(() => expect(screen.getAllByRole("listitem").length).toBe(2));
    expect(api.lookupSubscribers).toHaveBeenCalledWith("thandi", false);
    expect(screen.getByText(/1 Main Rd, Sandton/)).toBeTruthy();
    expect(screen.getByText(/…A1B2/)).toBeTruthy();
    expect(screen.getByText(/Soweto/)).toBeTruthy();
    expect(screen.getAllByText("VIP").length).toBe(1);
    expect(screen.getByText(/Several people can share a name/)).toBeTruthy();
  });
});
