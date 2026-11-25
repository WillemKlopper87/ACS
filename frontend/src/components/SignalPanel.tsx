// A cellular CPE's signal over time (RSRP, RSRQ, SNR, RSSI), drawn from the
// readings the ACS recorded at the device's check-ins (see
// GET /devices/{id}/signal-history). A line chart for the eye, and a table of
// the same rows for anyone who cannot rely on the chart or its colour.
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { SignalHistory } from "../api/types";
import { fmtTime } from "../lib/format";
import { buildSeries, type SignalMetric } from "../lib/signalSeries";

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

const NOTE_STYLE = { fontSize: "0.82rem", margin: 0 } as const;

function fmtValue(v: number | null): string {
  return v == null ? "—" : String(Math.round(v * 10) / 10);
}

export function SignalPanel({ deviceId }: { deviceId: string }) {
  const [hours, setHours] = useState(24 * 7);
  const [metric, setMetric] = useState<SignalMetric>("rsrp");
  const [table, setTable] = useState(false);
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
  const series = buildSeries(samples, metric, from, to, Math.max(data?.interval_seconds ?? 0, data?.bucket_seconds ?? 0));
  const window = Number.isFinite(from) && Number.isFinite(to) && to > from;

  return (
    <div className="panel">
      <h3>
        Signal
        <span style={{ display: "inline-flex", gap: "0.3rem", flexWrap: "wrap" }}>
          {RANGES.map((r) => (
            <button key={r.hours} className="btn sm" aria-pressed={hours === r.hours} disabled={hours === r.hours} onClick={() => setHours(r.hours)}>
              {r.label}
            </button>
          ))}
        </span>
      </h3>
      <div style={{ display: "flex", gap: "0.3rem", flexWrap: "wrap", margin: "0.2rem 0 0.6rem" }}>
        {METRICS.map((x) => (
          <button key={x.key} className="btn sm" aria-pressed={metric === x.key} disabled={metric === x.key} onClick={() => setMetric(x.key)}>
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
          {series.count === 0 ? (
            <p className="dim" style={NOTE_STYLE}>No readings for this metric on this device.</p>
          ) : (
            !table && (
              <>
                <div style={{ display: "flex", gap: "1rem", flexWrap: "wrap", fontSize: "0.78rem" }}>
                  <span>Highest {fmtValue(series.max)} {m.unit}</span>
                  <span>Lowest {fmtValue(series.min)} {m.unit}</span>
                </div>
                <svg
                  viewBox="0 0 1000 100"
                  role="img"
                  aria-label={`${m.label} over the last ${hours} hours: ${series.count} ${series.count === 1 ? "reading" : "readings"}, from ${fmtValue(series.min)} to ${fmtValue(series.max)} ${m.unit}`}
                  style={{ width: "100%", height: "160px", display: "block", margin: "0.4rem 0 0.2rem", background: "var(--panel-2, transparent)" }}
                  preserveAspectRatio="none"
                >
                  <line x1={0} y1={0} x2={1000} y2={0} stroke="var(--ink-dim)" strokeWidth={1} opacity={0.35} vectorEffect="non-scaling-stroke" />
                  <line x1={0} y1={100} x2={1000} y2={100} stroke="var(--ink-dim)" strokeWidth={1} opacity={0.35} vectorEffect="non-scaling-stroke" />
                  {series.segments.map((seg, i) =>
                    seg.length === 1 ? (
                      <circle key={i} cx={seg[0].x} cy={seg[0].y} r={3} fill="var(--accent)" vectorEffect="non-scaling-stroke" />
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
                {window && (
                  <div className="dim" style={{ display: "flex", justifyContent: "space-between", fontSize: "0.72rem" }}>
                    <span>{fmtTime(data.from)} UTC</span>
                    <span>{fmtTime(data.to)} UTC</span>
                  </div>
                )}
              </>
            )
          )}
          {table && series.count > 0 && (
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
