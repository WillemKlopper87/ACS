import { describe, expect, it } from "vitest";
import { skippedNote, unsupportedReason } from "./diagnostics";

describe("unsupportedReason", () => {
  it("offers a diagnostic whose capability is unknown or supported", () => {
    expect(unsupportedReason(null)).toBeNull();
    expect(unsupportedReason(undefined)).toBeNull();
    expect(unsupportedReason({ supported: true })).toBeNull();
  });

  it("explains why discovery rules a diagnostic out", () => {
    expect(unsupportedReason({ supported: false, reason: "Device.IP.Diagnostics.IPPing. was not found by parameter discovery" })).toBe(
      "Device.IP.Diagnostics.IPPing. was not found by parameter discovery",
    );
    expect(unsupportedReason({ supported: false })).toBe("not supported by this device");
  });
});

describe("skippedNote", () => {
  it("lists skipped inputs only when there are some", () => {
    expect(skippedNote(undefined)).toBe("");
    expect(skippedNote([])).toBe("");
    expect(skippedNote(["Timeout", "DSCP"])).toBe(" (device default used for Timeout, DSCP)");
  });
});
