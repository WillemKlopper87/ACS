import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { StatusBadge } from "./StatusBadge";

describe("StatusBadge", () => {
  it("renders the raw value as its text content", () => {
    const { container } = render(<StatusBadge value="ONLINE" />);
    expect(container.textContent).toBe("ONLINE");
  });

  it.each([
    ["ONLINE", "pill-ok"],
    ["SUCCESS", "pill-ok"],
    ["QUEUED", "pill-pending"],
    ["FAILED", "pill-danger"],
    ["OFFLINE", "pill-danger"],
    ["PERIODIC_FALLBACK_ONLY", "pill-warn"],
    ["GRACE", "pill-warn"],
    ["UNKNOWN", "pill-neutral"],
  ])("maps %s to tone class %s", (value, expectedClass) => {
    const { container } = render(<StatusBadge value={value} />);
    const span = container.querySelector("span");
    expect(span?.className).toContain(expectedClass);
  });

  it("falls back to the neutral tone for a value with no known mapping, rather than throwing", () => {
    const { container } = render(<StatusBadge value="SOME_FUTURE_STATUS_NOT_YET_MAPPED" />);
    const span = container.querySelector("span");
    expect(span?.className).toContain("pill-neutral");
    expect(container.textContent).toBe("SOME_FUTURE_STATUS_NOT_YET_MAPPED");
  });
});
