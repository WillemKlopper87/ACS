// Formatting and chart geometry for the Wi-Fi insights panel
// (docs/WIFI-MANAGEMENT-SPEC.md §3.3a). Pure functions, so the SVG charts
// in components/TimeSeriesChart stay declarative and testable.

/** Network throughput in decimal units (1 kbps = 1000 bps). */
export function fmtRate(bps: number): string {
  if (!Number.isFinite(bps) || bps < 0) return "—";
  if (bps < 1000) return `${Math.round(bps)} bps`;
  const units = ["kbps", "Mbps", "Gbps", "Tbps"];
  let value = bps / 1000;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit++;
  }
  return `${value < 10 ? Number(value.toFixed(1)) : Math.round(value)} ${units[unit]}`;
}

/** Seconds as the two largest units: "45s", "2m 5s", "3h 7m", "2d 5h". */
export function fmtDurationShort(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${sec}s`;
  return `${sec}s`;
}

/** Axis top: the smallest 1, 2, 2.5 or 5 x 10^k at or above the data max. */
export function niceMax(value: number): number {
  if (!(value > 0)) return 1;
  const p = 10 ** Math.floor(Math.log10(value));
  for (const m of [1, 2, 2.5, 5, 10]) {
    if (m * p >= value) return m * p;
  }
  return 10 * p;
}

const round2 = (n: number) => Math.round(n * 100) / 100;

/**
 * SVG path for one series across the plot width. A null value (no sample
 * in that bucket) breaks the line rather than drawing a false zero.
 */
export function linePath(values: (number | null)[], plot: { width: number; height: number; max: number }): string {
  const step = values.length > 1 ? plot.width / (values.length - 1) : 0;
  let d = "";
  let open = false;
  values.forEach((v, i) => {
    if (v === null || !Number.isFinite(v)) {
      open = false;
      return;
    }
    const x = round2(i * step);
    const y = round2(plot.height - (Math.min(v, plot.max) / plot.max) * plot.height);
    d += `${open ? "L" : "M"}${x},${y}`;
    open = true;
  });
  return d;
}

/** The sample index closest to a pointer x on a plot of the given width. */
export function nearestIndex(x: number, count: number, width: number): number {
  if (count <= 1) return 0;
  const i = Math.round(x / (width / (count - 1)));
  return Math.min(count - 1, Math.max(0, i));
}

/** Up to four evenly spaced sample indices for x-axis labels. */
export function timeTicks(count: number): number[] {
  if (count <= 0) return [];
  if (count <= 4) return Array.from({ length: count }, (_, i) => i);
  const step = (count - 1) / 3;
  return [0, 1, 2, 3].map((i) => Math.round(i * step));
}
