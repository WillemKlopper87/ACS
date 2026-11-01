import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { BulkOutcomes } from "./FleetControl";
import type { BulkActionResponse } from "../api/types";

// SET_WIFI resolves each device's own primary networks, so the console
// must show every device's outcome — which bands were written, which
// device only got the role read, which one needs discovery — not just a
// count and the first few errors.
describe("BulkOutcomes", () => {
  const result: BulkActionResponse = {
    action: "SET_WIFI",
    requested: 3,
    succeeded: 1,
    results: [
      { device_id: "11111111-aaaa", command_key: "ck-set", detail: "SSID 1 (2.4GHz) + SSID 5 (5GHz)" },
      {
        device_id: "22222222-bbbb",
        refresh_command_key: "ck-read",
        error: "Wi-Fi roles not known yet: queued a read of them, retry after the device's next session",
      },
      { device_id: "33333333-cccc", error: "run parameter discovery first" },
    ],
  };

  it("lists every device, failures first, with the networks written", () => {
    const { container } = render(<BulkOutcomes result={result} deviceName={(id) => (id === "11111111-aaaa" ? "FastMile lab" : undefined)} />);
    const rows = [...container.querySelectorAll("[data-outcome]")];
    expect(rows.map((r) => r.getAttribute("data-outcome"))).toEqual(["failed", "failed", "queued"]);
    expect(rows[2].textContent).toContain("FastMile lab");
    expect(rows[2].textContent).toContain("ck-set — SSID 1 (2.4GHz) + SSID 5 (5GHz)");
    expect(container.textContent).toContain("1 of 3 queued successfully");
  });

  it("names the read queued for a device whose Wi-Fi roles were unknown", () => {
    const { container } = render(<BulkOutcomes result={result} />);
    expect(container.textContent).toContain("(read ck-read)");
    expect(container.textContent).toContain("33333333… — run parameter discovery first");
  });
});
