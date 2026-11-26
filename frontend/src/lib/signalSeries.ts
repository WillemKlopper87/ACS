// Turns a cellular CPE's recorded signal readings into line segments for an
// inline SVG chart. Pure: no DOM, no fetching.
import type { SignalPoint } from "../api/types";

export type SignalMetric = "rsrp" | "rsrq" | "snr" | "rssi";

export interface SeriesPoint {
  /** 0..1000 across the window. */
  x: number;
  /** 0..100, 0 at the top for the largest value. */
  y: number;
  /** The reading's time, ms since the epoch, and its value, for a hover readout. */
  t: number;
  v: number;
}

export interface Series {
  segments: SeriesPoint[][];
  min: number;
  max: number;
  /** How many readings carry this metric. */
  count: number;
}

const W = 1000;
const H = 100;

/**
 * A reading with no value for the metric is skipped, never drawn as zero, and
 * it breaks the line. So does a silence longer than `max(2.5 * interval, 300)`
 * seconds: a line across a gap would claim readings nobody took.
 */
export function buildSeries(points: SignalPoint[], metric: SignalMetric, from: number, to: number, intervalSeconds: number): Series {
  const gapMs = Math.max(2.5 * (Number.isFinite(intervalSeconds) ? intervalSeconds : 0), 300) * 1000;
  const span = to - from;
  const usable: { t: number; v: number }[] = [];
  const broken = new Set<number>(); // indexes in `usable` that follow a skipped point
  let skipped = false;
  for (const p of points ?? []) {
    const v = p[metric];
    const t = new Date(p.at).getTime();
    if (v == null || !Number.isFinite(v) || !Number.isFinite(t)) {
      skipped = true;
      continue;
    }
    if (skipped) broken.add(usable.length);
    skipped = false;
    usable.push({ t, v });
  }
  if (usable.length === 0) return { segments: [], min: 0, max: 0, count: 0 };

  let min = Infinity;
  let max = -Infinity;
  for (const u of usable) {
    if (u.v < min) min = u.v;
    if (u.v > max) max = u.v;
  }
  const range = max - min;

  const segments: SeriesPoint[][] = [];
  let current: SeriesPoint[] = [];
  usable.forEach((u, i) => {
    if (i > 0 && (broken.has(i) || u.t - usable[i - 1].t > gapMs)) {
      segments.push(current);
      current = [];
    }
    const x = span > 0 ? Math.min(W, Math.max(0, ((u.t - from) / span) * W)) : 0;
    const y = range > 0 ? ((max - u.v) / range) * H : H / 2;
    current.push({ x, y, t: u.t, v: u.v });
  });
  segments.push(current);
  return { segments, min, max, count: usable.length };
}
