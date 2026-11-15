import { describe, expect, it } from "vitest";
import { plainLabel } from "./labels";

describe("plainLabel", () => {
  it("puts identifiers in an operator's words", () => {
    expect(plainLabel("PERIODIC_FALLBACK_ONLY")).toBe("Reached at its next check-in");
    expect(plainLabel("under_5m")).toBe("In the last 5 minutes");
    expect(plainLabel("stale")).toBe("More than a day ago");
  });

  it("leaves a value it does not know as it is", () => {
    expect(plainLabel("Zyxel")).toBe("Zyxel");
    expect(plainLabel("SOME_NEW_MODE")).toBe("SOME_NEW_MODE");
  });
});
