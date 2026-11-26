// A cellular CPE's signal over time (RSRP, RSRQ, SNR, RSSI), drawn from the
// readings the ACS recorded at the device's check-ins (see
// GET /devices/{id}/signal-history). A line chart for the eye, and a table of
// the same rows for anyone who cannot rely on the chart or its colour.
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { SignalHistory } from "../api/types";
import { fmtTime } from "../lib/format";
import { buildSeries, type SeriesPoint, type SignalMetric } from "../lib/signalSeries";

const RANGES = [
  { label: "24 hours", hours: 24 },
  { label: "7 days", hours: 24 * 7 },
  { label: "30 days", hours: 24 * 30 },
];

const METRICS: { key: SignalMetric; label: string; unit: string }[] = [
  { key: "rsrp", label: "RSRP", unit: "dBm" },
  { key: "rsrq", label: "RSRQ", unit: "dB" },
  { key: "snr", label: "SNR", unit: "dB" },
  { key: "rssi", label: "RSSI", unit: "dBm" },
];

// The chart's user units: x runs -12..1012 and y -8..108 (see the viewBox), so
// the plotted 0..1000 by 0..100 leaves room for the line's stroke and dots.
const VB_X0 = -12;
const VB_W = 1024;
const VB_Y0 = -8;
const VB_H = 116;
const PLOT_HEIGHT = "160px";
const GUTTER = "3.4rem";
const pctX = (x: number) => `${((x - VB_X0) / VB_W) * 100}%`;
const pctY = (y: number) => `${((y - VB_Y0) / VB_H) * 100}%`;

const NOTE_STYLE = { fontSize: "0.82rem", margin: 0 } as const;

function fmtValue(v: number | null): string {
  return v == null ? "—" : String(Math.round(v * 10) / 10);
}

export function SignalPanel({ deviceId }: { deviceId: string }) {
  const [hours, setHours] = useState(24 * 7);
  const [metric, setMetric] = useState<SignalMetric>("rsrp");
  const [table, setTable] = useState(false);
  // The point under the pointer. It belongs to the metric and range it was
  // read in, so it vanishes when either changes.
  const [hover, setHover] = useState<{ point: SeriesPoint; metric: SignalMetric; hours: number } | null>(null);
  const [data, setData] = useState<SignalHistory | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setData(null);
    setError(null);
    api.getSignalHistory(deviceId, hours).then(
      (d) => live && setData(d),
      () => live && setError("Could not load the signal history."),
    );
    return () => {
      live = false;
    };
  }, [deviceId, hours]);

  // A malformed reply must not blank the page this panel sits on.
  const samples = data?.samples ?? [];
  const from = data ? new Date(data.from).getTime() : 0;
  const to = data ? new Date(data.to).getTime() : 0;
  const m = METRICS.find((x) => x.key === metric) ?? METRICS[0];
  const rangeLabel = RANGES.find((r) => r.hours === hours)?.label ?? `${hours} hours`;
  const series = buildSeries(samples, metric, from, to, Math.max(data?.interval_seconds ?? 0, data?.bucket_seconds ?? 0));
  const hasWindow = Number.isFinite(from) && Number.isFinite(to) && to > from;
  const hovered = hover && hover.metric === metric && hover.hours === hours ? hover.point : null;
  // Levels on the y axis: the highest, the middle and the lowest reading (one
  // level, in the middle, when every reading is the same).
  const yTicks =
    series.count === 0
      ? []
      : series.max === series.min
        ? [{ y: 50, value: series.max }]
        : [
            { y: 0, value: series.max },
            { y: 50, value: (series.max + series.min) / 2 },
            { y: 100, value: series.min },
          ];

  // The reading nearest the pointer, by horizontal distance.
  const onPlotMove = (e: React.MouseEvent<HTMLDivElement>) => {
    const box = e.currentTarget.getBoundingClientRect();
    if (!(box.width > 0)) return;
    const ux = ((e.clientX - box.left) / box.width) * VB_W + VB_X0;
    let best: SeriesPoint | null = null;
    for (const seg of series.segments) {
      for (const p of seg) {
        if (best === null || Math.abs(p.x - ux) < Math.abs(best.x - ux)) best = p;
      }
    }
    if (best) setHover({ point: best, metric, hours });
  };

  return (
    <div className="panel">
      <h3>
        Signal
        <span style={{ display: "inline-flex", gap: "0.3rem", flexWrap: "wrap" }}>
          {RANGES.map((r) => (
            <button key={r.hours} className="btn sm" aria-pressed={hours === r.hours} onClick={() => setHours(r.hours)}>
              {r.label}
            </button>
          ))}
        </span>
      </h3>
      <div style={{ display: "flex", gap: "0.3rem", flexWrap: "wrap", margin: "0.2rem 0 0.6rem" }}>
        {METRICS.map((x) => (
          <button key={x.key} className="btn sm" aria-pressed={metric === x.key} onClick={() => setMetric(x.key)}>
            {x.label}
          </button>
        ))}
        {samples.length > 0 && (
          <button className="btn sm" aria-pressed={table} onClick={() => setTable((t) => !t)}>
            {table ? "Show as chart" : "Show as table"}
          </button>
        )}
      </div>
      {error && <p className="dim" style={NOTE_STYLE}>{error}</p>}
      {!error && !data && <p className="dim" style={NOTE_STYLE}>Loading…</p>}
      {data && samples.length === 0 && (
        <p className="dim" style={NOTE_STYLE}>
          No signal samples yet. The ACS records a reading when a cellular CPE checks in, at most once per sample interval (15 minutes by
          default), so a new device or one that reports over USP has none until then.
        </p>
      )}
      {data && samples.length > 0 && (
        <>
          {!table && series.count === 0 && (
            <p className="dim" style={NOTE_STYLE}>No readings for this metric on this device.</p>
          )}
          {!table && series.count > 0 && (
            (
              <>
                <div style={{ display: "flex", gap: "1rem", flexWrap: "wrap", fontSize: "0.78rem" }}>
                  <span>Highest {fmtValue(series.max)} {m.unit}</span>
                  <span>Lowest {fmtValue(series.min)} {m.unit}</span>
                </div>
                <div style={{ minHeight: "1.2rem", fontSize: "0.82rem", margin: "0.3rem 0 0.1rem", paddingLeft: GUTTER }}>
                  {hovered && (
                    <strong data-testid="signal-readout">
                      {fmtValue(hovered.v)} {m.unit} · {fmtTime(new Date(hovered.t).toISOString())} UTC
                    </strong>
                  )}
                </div>
                <div style={{ display: "flex", gap: "0.35rem" }}>
                  <div style={{ width: GUTTER, flex: "none" }} aria-hidden="true">
                    <div className="dim" data-axis-unit="y" style={{ fontSize: "0.68rem", textAlign: "right", height: "0.9rem" }}>
                      {m.unit}
                    </div>
                    <div style={{ position: "relative", height: PLOT_HEIGHT }}>
                      {yTicks.map((t) => (
                        <span
                          key={t.y}
                          className="dim"
                          data-tick="y"
                          style={{ position: "absolute", right: 0, top: pctY(t.y), transform: "translateY(-50%)", fontSize: "0.7rem", lineHeight: 1 }}
                        >
                          {fmtValue(t.value)}
                        </span>
                      ))}
                    </div>
                  </div>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ height: "0.9rem" }} />
                    <div
                      data-testid="signal-plot"
                      style={{ position: "relative", height: PLOT_HEIGHT, cursor: "crosshair" }}
                      onMouseMove={onPlotMove}
                      onMouseLeave={() => setHover(null)}
                    >
                      <svg
                        viewBox={`${VB_X0} ${VB_Y0} ${VB_W} ${VB_H}`}
                        overflow="visible"
                        role="img"
                        aria-label={`${m.label} over the last ${rangeLabel}: ${series.count} ${series.count === 1 ? "reading" : "readings"}, from ${fmtValue(series.min)} to ${fmtValue(series.max)} ${m.unit}`}
                        style={{ width: "100%", height: "100%", display: "block", background: "var(--panel-2, transparent)" }}
                        preserveAspectRatio="none"
                      >
                        {yTicks.map((t) => (
                          <line key={t.y} x1={0} y1={t.y} x2={1000} y2={t.y} stroke="var(--ink-dim)" strokeWidth={1} opacity={t.y === 50 ? 0.18 : 0.35} vectorEffect="non-scaling-stroke" />
                        ))}
                        {series.segments.map((seg, i) =>
                          seg.length === 1 ? (
                            // A zero-length polyline with a round cap and a non-scaling stroke
                            // is a dot of fixed on-screen size however the SVG is stretched.
                            <polyline
                              key={i}
                              data-dot="true"
                              points={`${seg[0].x.toFixed(1)},${seg[0].y.toFixed(1)} ${seg[0].x.toFixed(1)},${seg[0].y.toFixed(1)}`}
                              fill="none"
                              stroke="var(--accent)"
                              strokeWidth={7}
                              strokeLinecap="round"
                              vectorEffect="non-scaling-stroke"
                            />
                          ) : (
                            <polyline
                              key={i}
                              points={seg.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(" ")}
                              fill="none"
                              stroke="var(--accent)"
                              strokeWidth={1.5}
                              strokeLinejoin="round"
                              vectorEffect="non-scaling-stroke"
                            />
                          ),
                        )}
                      </svg>
                      {hovered && (
                        <>
                          <div aria-hidden="true" style={{ position: "absolute", top: 0, bottom: 0, left: pctX(hovered.x), width: 1, background: "var(--ink-dim)", opacity: 0.5, pointerEvents: "none" }} />
                          <div
                            data-testid="signal-marker"
                            aria-hidden="true"
                            style={{ position: "absolute", left: pctX(hovered.x), top: pctY(hovered.y), width: 10, height: 10, borderRadius: "50%", background: "var(--accent)", border: "2px solid var(--panel, #fff)", transform: "translate(-50%, -50%)", pointerEvents: "none" }}
                          />
                        </>
                      )}
                    </div>
                    {hasWindow && (
                      <div className="dim" style={{ display: "flex", justifyContent: "space-between", fontSize: "0.72rem", marginTop: "0.2rem" }}>
                        <span data-tick="x">{fmtTime(data.from)} UTC</span>
                        <span data-tick="x">{fmtTime(new Date((from + to) / 2).toISOString())} UTC</span>
                        <span data-tick="x">{fmtTime(data.to)} UTC</span>
                      </div>
                    )}
                  </div>
                </div>
              </>
            )
          )}
          {table && (
            <div style={{ maxHeight: "22rem", overflow: "auto" }}>
              <table className="table" style={{ width: "100%", fontSize: "0.8rem" }}>
                <thead>
                  <tr>
                    <th style={{ textAlign: "left" }}>Time (UTC)</th>
                    <th style={{ textAlign: "left" }}>RSRP (dBm)</th>
                    <th style={{ textAlign: "left" }}>RSRQ (dB)</th>
                    <th style={{ textAlign: "left" }}>SNR (dB)</th>
                    <th style={{ textAlign: "left" }}>RSSI (dBm)</th>
                    <th style={{ textAlign: "left" }}>Band</th>
                    <th style={{ textAlign: "left" }}>Technology</th>
                  </tr>
                </thead>
                <tbody>
                  {[...samples].reverse().map((s, i) => (
                    <tr key={`${s.at}-${i}`}>
                      <td>{fmtTime(s.at)}</td>
                      <td>{fmtValue(s.rsrp)}</td>
                      <td>{fmtValue(s.rsrq)}</td>
                      <td>{fmtValue(s.snr)}</td>
                      <td>{fmtValue(s.rssi)}</td>
                      <td>{s.band || "—"}</td>
                      <td>{s.rat || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <p className="dim" style={{ fontSize: "0.74rem", margin: "0.5rem 0 0" }}>
            {"Times are UTC. The resolution is the larger of the sample interval and the CPE's check-in interval: the ACS only learns a reading when the device checks in. "}
            {data.bucket_seconds > 0
              ? `Over a long window the readings are averaged into ${Math.round(data.bucket_seconds / 60)}-minute steps. `
              : ""}
            A gap in the line means no reading was recorded then. RSRP and RSSI are in dBm, RSRQ and SNR in dB; closer to zero is stronger.
          </p>
        </>
      )}
    </div>
  );
}
