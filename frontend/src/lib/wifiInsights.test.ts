import { describe, expect, it } from "vitest";
import { fmtDurationShort, fmtRate, linePath, nearestIndex, niceMax, timeTicks } from "./wifiInsights";

describe("fmtRate", () => {
  it("formats bits per second in decimal network units", () => {
    expect(fmtRate(0)).toBe("0 bps");
    expect(fmtRate(950)).toBe("950 bps");
    expect(fmtRate(1500)).toBe("1.5 kbps");
    expect(fmtRate(12_400_000)).toBe("12 Mbps");
    expect(fmtRate(2_300_000_000)).toBe("2.3 Gbps");
    expect(fmtRate(Number.NaN)).toBe("—");
  });
});

describe("fmtDurationShort", () => {
  it("formats seconds as the two largest units", () => {
    expect(fmtDurationShort(0)).toBe("0s");
    expect(fmtDurationShort(45)).toBe("45s");
    expect(fmtDurationShort(125)).toBe("2m 5s");
    expect(fmtDurationShort(3 * 3600 + 7 * 60)).toBe("3h 7m");
    expect(fmtDurationShort(2 * 86400 + 5 * 3600)).toBe("2d 5h");
  });
});

describe("niceMax", () => {
  it("rounds the axis top up to 1, 2, 2.5 or 5 times a power of ten", () => {
    expect(niceMax(0)).toBe(1);
    expect(niceMax(7)).toBe(10);
    expect(niceMax(11)).toBe(20);
    expect(niceMax(230)).toBe(250);
    expect(niceMax(4100)).toBe(5000);
  });
});

describe("linePath", () => {
  it("maps values onto the plot and breaks the line at missing samples", () => {
    const d = linePath([0, 5, null, 10], { width: 30, height: 10, max: 10 });
    expect(d).toBe("M0,10L10,5M30,0");
  });
});

describe("nearestIndex", () => {
  it("snaps a pointer x to the closest sample", () => {
    expect(nearestIndex(0, 4, 300)).toBe(0);
    expect(nearestIndex(140, 4, 300)).toBe(1);
    expect(nearestIndex(299, 4, 300)).toBe(3);
    expect(nearestIndex(500, 4, 300)).toBe(3);
    expect(nearestIndex(10, 1, 300)).toBe(0);
  });
});

describe("timeTicks", () => {
  it("picks at most four evenly spaced sample indices", () => {
    expect(timeTicks(1)).toEqual([0]);
    expect(timeTicks(3)).toEqual([0, 1, 2]);
    expect(timeTicks(25)).toEqual([0, 8, 16, 24]);
  });
});
