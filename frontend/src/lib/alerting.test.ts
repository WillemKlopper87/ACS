import { describe, expect, it } from "vitest";
import { fmtSeconds, policyThresholdSeconds } from "./alerting";

describe("alert policy durations", () => {
  it("reads the API's nanoseconds as seconds", () => {
    expect(policyThresholdSeconds(900e9)).toBe(900);
    expect(policyThresholdSeconds("900000000000")).toBe(900);
    expect(policyThresholdSeconds(undefined)).toBe(0);
    expect(policyThresholdSeconds("nonsense")).toBe(0);
  });

  it("writes a duration the way a person says it", () => {
    expect(fmtSeconds(0)).toBe("—");
    expect(fmtSeconds(45)).toBe("45s");
    expect(fmtSeconds(900)).toBe("15m");
    expect(fmtSeconds(3600)).toBe("1h");
    expect(fmtSeconds(5430)).toBe("1h 30m 30s");
  });
});
