// A device's availability over time: a bar from the start of the window to
// now, with each stretch in which the device stopped reporting to the ACS drawn
// on it. The ACS only sees silence (see GET /devices/{id}/connectivity), so a
// stretch is "at most" this long, and its kind says what the first Inform back
// carried: a restart code (power or reboot) or none (the CPE stayed up).
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { ConnectivityOutage, DeviceConnectivity } from "../api/types";
import { fmtDuration, fmtTime } from "../lib/format";

const RANGES = [
  { label: "24 hours", hours: 24 },
  { label: "7 days", hours: 24 * 7 },
  { label: "30 days", hours: 24 * 30 },
];

const KIND_LABEL: Record<ConnectivityOutage["kind"], string> = {
  restart: "Restarted",
  connectivity: "Lost contact, CPE stayed up",
  ongoing: "Silent now",
};

const KIND_COLOUR: Record<ConnectivityOutage["kind"], string> = {
  restart: "var(--warn)",
  connectivity: "var(--danger)",
  ongoing: "var(--danger)",
};

const W = 1000;

/** Where a time falls on the bar, 0..W, clamped to the window. */
export function barX(iso: string | null, from: number, to: number): number {
  const t = iso ? new Date(iso).getTime() : to;
  return Math.min(W, Math.max(0, ((t - from) / (to - from)) * W));
}

export function ConnectivityTimeline({ deviceId }: { deviceId: string }) {
  const [hours, setHours] = useState(24 * 7);
  const [data, setData] = useState<DeviceConnectivity | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setData(null);
    setError(null);
    api.getDeviceConnectivity(deviceId, hours).then(
      (d) => live && setData(d),
      () => live && setError("Could not load the availability history."),
    );
    return () => {
      live = false;
    };
  }, [deviceId, hours]);

  const from = data ? new Date(data.from).getTime() : 0;
  const to = data ? new Date(data.to).getTime() : 0;
  // A malformed reply must not blank the page this panel sits on.
  const outages = data?.outages ?? [];
  const recent = [...outages].reverse().slice(0, 20);
  const longest = Math.max(0, ...outages.map((o) => o.seconds));

  return (
    <div className="panel">
      <h3>
        Availability
        <span style={{ display: "inline-flex", gap: "0.3rem" }}>
          {RANGES.map((r) => (
            <button key={r.hours} className="btn sm" aria-pressed={hours === r.hours} disabled={hours === r.hours} onClick={() => setHours(r.hours)}>
              {r.label}
            </button>
          ))}
        </span>
      </h3>
      {error && <p className="dim" style={{ fontSize: "0.82rem", margin: 0 }}>{error}</p>}
      {!error && !data && <p className="dim" style={{ fontSize: "0.82rem", margin: 0 }}>Loading…</p>}
      {data && data.checkins === 0 && (
        <p className="dim" style={{ fontSize: "0.82rem", margin: 0 }}>
          No check-ins recorded in this window. A device that reports over USP opens no CWMP sessions and has no history here.
        </p>
      )}
      {data && data.checkins > 0 && (
        <>
          <dl className="kv">
            <dt>Availability</dt>
            <dd>{data.availability_percent == null ? "—" : `${data.availability_percent.toFixed(2)}%`}</dd>
            <dt>Outages</dt>
            <dd>
              {outages.length}
              {longest > 0 && ` · longest up to ${fmtDuration(longest)}`}
            </dd>
            <dt>Reports every</dt>
            <dd>
              about {fmtDuration(data.interval_seconds)} · an outage is silence over {fmtDuration(data.threshold_seconds)}
            </dd>
          </dl>
          <svg
            viewBox={`0 0 ${W} 44`}
            role="img"
            aria-label={`Availability over the last ${hours} hours: ${outages.length} outages`}
            style={{ width: "100%", height: "44px", display: "block", margin: "0.6rem 0 0.2rem" }}
            preserveAspectRatio="none"
          >
            <rect x={0} y={4} width={W} height={36} rx={4} fill="var(--ok)" opacity={0.85} />
            {outages.map((o, i) => {
              const x1 = barX(o.last_seen, from, to);
              const x2 = barX(o.back_at, from, to);
              return (
                <rect key={i} x={x1} y={4} width={Math.max(2, x2 - x1)} height={36} fill={KIND_COLOUR[o.kind]}>
                  <title>{`${KIND_LABEL[o.kind]}: up to ${fmtDuration(o.seconds)}, from ${fmtTime(o.last_seen)}${o.back_at ? ` to ${fmtTime(o.back_at)}` : ""} UTC`}</title>
                </rect>
              );
            })}
          </svg>
          <div className="dim" style={{ display: "flex", justifyContent: "space-between", fontSize: "0.72rem" }}>
            <span>{fmtTime(data.from)} UTC</span>
            <span>now</span>
          </div>
          <div style={{ display: "flex", gap: "1rem", flexWrap: "wrap", fontSize: "0.76rem", margin: "0.5rem 0" }}>
            <span><span style={{ color: "var(--ok)" }}>■</span> Reporting</span>
            <span><span style={{ color: "var(--danger)" }}>■</span> Lost contact (or silent now)</span>
            <span><span style={{ color: "var(--warn)" }}>■</span> Restarted</span>
          </div>
          {recent.length > 0 && (
            <table className="table" style={{ width: "100%", fontSize: "0.8rem" }}>
              <thead>
                <tr>
                  <th style={{ textAlign: "left" }}>Last heard</th>
                  <th style={{ textAlign: "left" }}>Back</th>
                  <th style={{ textAlign: "left" }}>Silent for (at most)</th>
                  <th style={{ textAlign: "left" }}>What the device said</th>
                </tr>
              </thead>
              <tbody>
                {recent.map((o) => (
                  <tr key={o.last_seen}>
                    <td>{fmtTime(o.last_seen)}</td>
                    <td>{o.back_at ? fmtTime(o.back_at) : "—"}</td>
                    <td>{fmtDuration(o.seconds)}</td>
                    <td>{KIND_LABEL[o.kind]}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <p className="dim" style={{ fontSize: "0.74rem", margin: "0.5rem 0 0" }}>
            Times are UTC. This is built from when the device checked in, so it shows silence, not the exact moment the WAN dropped: while the
            link is down the device cannot tell the ACS. &ldquo;Restarted&rdquo; means the first check-in back carried a restart code; &ldquo;Lost
            contact&rdquo; means it did not, so the CPE stayed powered and its path to the ACS (WAN, radio, or the ACS itself) was down.
          </p>
        </>
      )}
    </div>
  );
}
