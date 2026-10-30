// Small multi-series line chart for sampled telemetry (Wi-Fi insights).
// One y-axis, 2px lines, a legend for two or more series plus a value
// label at each line's end, a crosshair tooltip that also follows the
// arrow keys, and a table view so no value depends on hovering. Series
// colors come from the --series-N tokens in index.css (validated per theme).
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { linePath, nearestIndex, niceMax, timeTicks } from "../lib/wifiInsights";

export interface ChartSeries {
  key: string;
  label: string;
  /** CSS custom property holding the series color, e.g. "--series-1". */
  color: string;
}

export interface ChartPoint {
  at: string;
  values: Record<string, number | null>;
}

interface Props {
  title: string;
  series: ChartSeries[];
  points: ChartPoint[];
  format: (value: number) => string;
  formatTime: (iso: string) => string;
  height?: number;
}

const PAD = { top: 8, right: 92, bottom: 22, left: 64 };
const LABEL_GAP = 13;

export function TimeSeriesChart({ title, series, points, format, formatTime, height = 170 }: Props) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(640);
  const [hover, setHover] = useState<number | null>(null);
  const [showTable, setShowTable] = useState(false);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([entry]) => setWidth(Math.max(280, Math.round(entry.contentRect.width))));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const plotW = width - PAD.left - PAD.right;
  const plotH = height - PAD.top - PAD.bottom;
  let dataMax = 0;
  for (const p of points) for (const s of series) dataMax = Math.max(dataMax, p.values[s.key] ?? 0);
  const max = niceMax(dataMax);
  const xAt = (i: number) => (points.length > 1 ? (i * plotW) / (points.length - 1) : plotW / 2);
  const yAt = (v: number) => plotH - (Math.min(v, max) / max) * plotH;

  // End labels: each series' last value, nudged apart so they never overlap.
  const ends = series
    .map((s) => {
      let i = points.length - 1;
      while (i >= 0 && points[i].values[s.key] == null) i--;
      return i < 0 ? null : { s, v: points[i].values[s.key] as number, y: yAt(points[i].values[s.key] as number) };
    })
    .filter((e): e is { s: ChartSeries; v: number; y: number } => e !== null)
    .sort((a, b) => a.y - b.y);
  for (let k = 1; k < ends.length; k++) ends[k].y = Math.max(ends[k].y, ends[k - 1].y + LABEL_GAP);

  const onPointer = (clientX: number, rect: DOMRect) => {
    const x = ((clientX - rect.left) / rect.width) * width - PAD.left;
    setHover(nearestIndex(x, points.length, plotW));
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    setHover((h) => {
      const cur = h ?? (e.key === "ArrowLeft" ? points.length : -1);
      return Math.min(points.length - 1, Math.max(0, cur + (e.key === "ArrowRight" ? 1 : -1)));
    });
  };

  const hovered = hover !== null ? points[hover] : null;
  const tipLeft = hover !== null ? ((PAD.left + xAt(hover)) / width) * 100 : 0;

  return (
    <figure className="viz">
      <figcaption className="viz-head">
        <span className="viz-title">{title}</span>
        {series.length > 1 && (
          <span className="viz-legend">
            {series.map((s) => (
              <span key={s.key}>
                <i style={{ background: `var(${s.color})` }} aria-hidden="true" />
                {s.label}
              </span>
            ))}
          </span>
        )}
        <button className="btn sm viz-toggle" onClick={() => setShowTable((v) => !v)}>
          {showTable ? "Show as chart" : "Show as table"}
        </button>
      </figcaption>

      {showTable ? (
        <div className="table-wrap viz-table">
          <table aria-label={`${title} values`}>
            <thead>
              <tr>
                <th>Time</th>
                {series.map((s) => (
                  <th key={s.key}>{s.label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {points.map((p) => (
                <tr key={p.at}>
                  <td>{formatTime(p.at)}</td>
                  {series.map((s) => (
                    <td key={s.key}>{p.values[s.key] == null ? "—" : format(p.values[s.key] as number)}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="viz-plot" ref={wrapRef}>
          <svg
            role="img"
            aria-label={`${title}, ${points.length} samples. Use the arrow keys to read values, or show as table.`}
            tabIndex={0}
            width="100%"
            viewBox={`0 0 ${width} ${height}`}
            onPointerMove={(e) => onPointer(e.clientX, e.currentTarget.getBoundingClientRect())}
            onPointerLeave={() => setHover(null)}
            onKeyDown={onKey}
            onBlur={() => setHover(null)}
          >
            <g transform={`translate(${PAD.left},${PAD.top})`}>
              {[0, 0.5, 1].map((f) => (
                <g key={f} className="viz-grid">
                  <line x1={0} x2={plotW} y1={plotH - f * plotH} y2={plotH - f * plotH} />
                  <text x={-8} y={plotH - f * plotH} dy="0.32em" textAnchor="end">
                    {format(f * max)}
                  </text>
                </g>
              ))}
              {timeTicks(points.length).map((i) => (
                <text key={i} className="viz-axis" x={xAt(i)} y={plotH + 16} textAnchor={i === 0 ? "start" : i === points.length - 1 ? "end" : "middle"}>
                  {formatTime(points[i].at)}
                </text>
              ))}
              {series.map((s) => (
                <path
                  key={s.key}
                  className="viz-line"
                  style={{ stroke: `var(${s.color})` }}
                  d={linePath(points.map((p) => p.values[s.key] ?? null), { width: plotW, height: plotH, max })}
                />
              ))}
              {points.length === 1 &&
                series.map((s) =>
                  points[0].values[s.key] == null ? null : (
                    <circle key={s.key} cx={xAt(0)} cy={yAt(points[0].values[s.key] as number)} r={4} style={{ fill: `var(${s.color})` }} />
                  ),
                )}
              {ends.map(({ s, v, y }) => (
                <text key={s.key} className="viz-end" x={plotW + 8} y={y} dy="0.32em">
                  {series.length > 1 ? `${s.label} ` : ""}
                  <tspan className="viz-end-v">{format(v)}</tspan>
                </text>
              ))}
              {hover !== null && (
                <g className="viz-cross">
                  <line x1={xAt(hover)} x2={xAt(hover)} y1={0} y2={plotH} />
                  {series.map((s) =>
                    points[hover].values[s.key] == null ? null : (
                      <circle key={s.key} cx={xAt(hover)} cy={yAt(points[hover].values[s.key] as number)} r={4} style={{ fill: `var(${s.color})` }} />
                    ),
                  )}
                </g>
              )}
            </g>
          </svg>
          {hovered && (
            <div className={`viz-tip ${tipLeft > 60 ? "left" : ""}`} style={{ left: `${tipLeft}%` }} role="status">
              <div className="viz-tip-time">{formatTime(hovered.at)}</div>
              {series.map((s) => (
                <div key={s.key} className="viz-tip-row">
                  <i style={{ background: `var(${s.color})` }} aria-hidden="true" />
                  <strong>{hovered.values[s.key] == null ? "—" : format(hovered.values[s.key] as number)}</strong>
                  <span>{s.label}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </figure>
  );
}
