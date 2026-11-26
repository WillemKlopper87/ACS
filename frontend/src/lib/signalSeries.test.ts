import { describe, expect, it } from "vitest";
import type { SignalPoint } from "../api/types";
import { buildSeries } from "./signalSeries";

const from = Date.parse("2026-11-20T00:00:00Z");
const to = Date.parse("2026-11-21T00:00:00Z");

function pt(at: string, rsrp: number | null, extra: Partial<SignalPoint> = {}): SignalPoint {
  return { at, rsrp, rsrq: null, snr: null, rssi: null, band: "", rat: "", ...extra };
}

describe("buildSeries", () => {
  it("joins two close points into one segment, largest value at the top", () => {
    const s = buildSeries([pt("2026-11-20T06:00:00Z", -100), pt("2026-11-20T06:15:00Z", -90)], "rsrp", from, to, 900);
    expect(s.segments).toHaveLength(1);
    expect(s.segments[0]).toHaveLength(2);
    expect(s.segments[0][0].x).toBe(250);
    expect(s.segments[0][0].y).toBe(100);
    expect(s.segments[0][1].y).toBe(0);
    expect(s.min).toBe(-100);
    expect(s.max).toBe(-90);
    expect(s.count).toBe(2);
  });

  it("breaks the line at a gap longer than 2.5 intervals", () => {
    const s = buildSeries([pt("2026-11-20T01:00:00Z", -100), pt("2026-11-20T05:00:00Z", -95)], "rsrp", from, to, 900);
    expect(s.segments).toHaveLength(2);
  });

  it("uses a floor of five minutes when the interval is unknown", () => {
    const joined = buildSeries([pt("2026-11-20T01:00:00Z", -100), pt("2026-11-20T01:04:00Z", -95)], "rsrp", from, to, 0);
    expect(joined.segments).toHaveLength(1);
    const split = buildSeries([pt("2026-11-20T01:00:00Z", -100), pt("2026-11-20T01:06:00Z", -95)], "rsrp", from, to, 0);
    expect(split.segments).toHaveLength(2);
  });

  it("skips a null reading and splits around it", () => {
    const s = buildSeries(
      [pt("2026-11-20T01:00:00Z", -100), pt("2026-11-20T01:15:00Z", null), pt("2026-11-20T01:30:00Z", -95)],
      "rsrp",
      from,
      to,
      900,
    );
    expect(s.segments).toHaveLength(2);
    expect(s.count).toBe(2);
  });

  it("draws a single point as a segment of one", () => {
    const s = buildSeries([pt("2026-11-20T12:00:00Z", -100)], "rsrp", from, to, 900);
    expect(s.segments).toHaveLength(1);
    expect(s.segments[0]).toHaveLength(1);
  });

  it("returns a count of 0 for no points, or none with this metric", () => {
    expect(buildSeries([], "rsrp", from, to, 900).count).toBe(0);
    const s = buildSeries([pt("2026-11-20T12:00:00Z", null)], "rsrp", from, to, 900);
    expect(s.count).toBe(0);
    expect(s.segments).toEqual([]);
  });

  it("never gives NaN for a flat series", () => {
    const s = buildSeries([pt("2026-11-20T01:00:00Z", -100), pt("2026-11-20T01:15:00Z", -100)], "rsrp", from, to, 900);
    for (const p of s.segments[0]) {
      expect(Number.isNaN(p.y)).toBe(false);
      expect(Number.isNaN(p.x)).toBe(false);
    }
    expect(s.min).toBe(-100);
    expect(s.max).toBe(-100);
  });

  it("reads the metric asked for", () => {
    const s = buildSeries([pt("2026-11-20T01:00:00Z", -100, { snr: 12 })], "snr", from, to, 900);
    expect(s.min).toBe(12);
  });
});
