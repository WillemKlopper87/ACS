import { describe, it, expect } from "vitest";
import { timeAgo, fmtTime, durationSeconds, fmtBytes } from "./format";

describe("timeAgo", () => {
  it("returns an em dash for an undefined timestamp", () => {
    expect(timeAgo(undefined)).toBe("—");
  });

  it("says 'just now' for anything under 5 seconds", () => {
    const now = new Date(Date.now() - 2000).toISOString();
    expect(timeAgo(now)).toBe("just now");
  });

  it("formats seconds, minutes, hours, and days at the right thresholds", () => {
    expect(timeAgo(new Date(Date.now() - 30_000).toISOString())).toBe("30s ago");
    expect(timeAgo(new Date(Date.now() - 5 * 60_000).toISOString())).toBe("5m ago");
    expect(timeAgo(new Date(Date.now() - 3 * 3600_000).toISOString())).toBe("3h ago");
    expect(timeAgo(new Date(Date.now() - 2 * 86400_000).toISOString())).toBe("2d ago");
  });
});

describe("fmtTime", () => {
  it("returns an em dash for an undefined timestamp", () => {
    expect(fmtTime(undefined)).toBe("—");
  });

  it("strips the T/Z ISO markers and truncates to seconds precision", () => {
    expect(fmtTime("2026-08-11T14:30:05.123456Z")).toBe("2026-08-11 14:30:05");
  });
});

describe("durationSeconds", () => {
  it("returns null when there's no completedAt (job still in flight)", () => {
    expect(durationSeconds("2026-08-11T14:30:00Z", undefined)).toBeNull();
  });

  it("computes the elapsed seconds between created and completed, rounded to 1 decimal", () => {
    expect(durationSeconds("2026-08-11T14:30:00.000Z", "2026-08-11T14:30:02.500Z")).toBe(2.5);
  });

  it("handles a sub-second job (completed in the same second)", () => {
    expect(durationSeconds("2026-08-11T14:30:00.000Z", "2026-08-11T14:30:00.250Z")).toBe(0.3);
  });
});

describe("fmtBytes", () => {
  it("keeps raw bytes below 1 KiB", () => {
    expect(fmtBytes(0)).toBe("0 B");
    expect(fmtBytes(1023)).toBe("1023 B");
  });

  it("steps up through binary units", () => {
    expect(fmtBytes(1024)).toBe("1.0 KiB");
    expect(fmtBytes(1536)).toBe("1.5 KiB");
    expect(fmtBytes(1024 * 1024)).toBe("1.0 MiB");
    expect(fmtBytes(1024 ** 3)).toBe("1.0 GiB");
  });

  it("drops the decimal once the value is large enough not to need it", () => {
    expect(fmtBytes(1024 * 15)).toBe("15 KiB");
  });

  it("does not run past the largest unit it knows", () => {
    expect(fmtBytes(1024 ** 6)).toMatch(/TiB$/);
  });

  it("returns a dash rather than NaN for a nonsense size", () => {
    expect(fmtBytes(Number.NaN)).toBe("—");
    expect(fmtBytes(-1)).toBe("—");
  });
});
