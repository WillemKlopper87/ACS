import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { StatusBadge } from "./StatusBadge";

describe("StatusBadge", () => {
  it("renders the raw value as its text content when it needs no relabelling", () => {
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

  // A non-repeatable job whose lease expired is never retried, so the
  // work did not run and will not run. Rendering that neutral-grey next
  // to genuinely idle rows is how it gets missed; it must read as a
  // failure.
  it.each([
    ["LEASE_EXPIRED_UNSAFE_RETRY", "pill-danger"],
    ["AWAITING_TRANSFER_COMPLETE", "pill-pending"],
  ])("does not leave %s on the neutral fallback", (value, expectedClass) => {
    const { container } = render(<StatusBadge value={value} />);
    expect(container.querySelector("span")?.className).toContain(expectedClass);
  });

  it("shows a readable label for mouthful wire values, keeping the raw value on hover", () => {
    const { container } = render(<StatusBadge value="HTTP_200_INFORM_RECEIVED" />);
    const span = container.querySelector("span");
    expect(span?.textContent).toBe("inform received");
    expect(span?.getAttribute("title")).toBe("HTTP_200_INFORM_RECEIVED");
  });

  it("falls back to the neutral tone for a value with no known mapping, rather than throwing", () => {
    const { container } = render(<StatusBadge value="SOME_FUTURE_STATUS_NOT_YET_MAPPED" />);
    const span = container.querySelector("span");
    expect(span?.className).toContain("pill-neutral");
    expect(container.textContent).toBe("SOME_FUTURE_STATUS_NOT_YET_MAPPED");
  });
});
