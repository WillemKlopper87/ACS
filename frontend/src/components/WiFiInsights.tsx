// Wi-Fi insights (docs/WIFI-MANAGEMENT-SPEC.md §3.3a, Grandstream
// GDMS-style): bandwidth, clients and SSID usage that the ACS builds by
// sampling the gateway's counters on periodic Informs. The CPE keeps no
// history itself, so data starts when sampling was switched on. A mesh
// satellite has no Device.WiFi tree: its clients appear on the gateway.
import { useEffect, useState, type ReactNode } from "react";
import { api } from "../api/client";
import type { WiFiInsightsData, WiFiInsightsWindow } from "../api/client";
import { fmtBytes, timeAgo } from "../lib/format";
import { fmtDurationShort, fmtRate } from "../lib/wifiInsights";
import { TimeSeriesChart } from "./TimeSeriesChart";

const WINDOWS: { id: WiFiInsightsWindow; short: string; label: string }[] = [
  { id: "2h", short: "2h", label: "2 hours" },
  { id: "1d", short: "1d", label: "1 day" },
  { id: "1w", short: "1w", label: "1 week" },
  { id: "1m", short: "1m", label: "1 month" },
];

// Samples arrive every few minutes; re-reading faster only adds load.
const REFRESH_MS = 60_000;

const BANDWIDTH_SERIES = [
  { key: "down", label: "Download", color: "--series-1" },
  { key: "up", label: "Upload", color: "--series-2" },
];
const CLIENT_SERIES = [
  { key: "wireless", label: "Wireless", color: "--series-1" },
  { key: "wired", label: "Wired", color: "--series-2" },
  { key: "guest", label: "Guest", color: "--series-3" },
];

function timeFormatter(range: WiFiInsightsWindow): (iso: string) => string {
  const opts: Intl.DateTimeFormatOptions =
    range === "2h" || range === "1d" ? { hour: "2-digit", minute: "2-digit" } : { day: "numeric", month: "short", hour: "2-digit" };
  return (iso) => new Date(iso).toLocaleString(undefined, opts);
}

function ShareList({ label, items, valueOf, render }: {
  label: string;
  items: { key: string; value: number }[];
  valueOf: (value: number) => string;
  render: (key: string) => ReactNode;
}) {
  const max = Math.max(1, ...items.map((i) => i.value));
  return (
    <div className="share">
      <h4>{label}</h4>
      {items.length === 0 ? (
        <p className="dim share-empty">None in this window.</p>
      ) : (
        <ul aria-label={label}>
          {items.map((i) => (
            <li key={i.key}>
              <span className="share-name">{render(i.key)}</span>
              <span className="share-v">{valueOf(i.value)}</span>
              <span className="share-bar" aria-hidden="true">
                <span style={{ width: `${(i.value / max) * 100}%` }} />
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function WiFiInsights({ deviceId, satelliteOf }: { deviceId: string; satelliteOf?: string }) {
  const [range, setRange] = useState<WiFiInsightsWindow>("2h");
  const [data, setData] = useState<WiFiInsightsData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (satelliteOf !== undefined) return;
    let cancelled = false;
    const load = () => {
      setLoading(true);
      api.getWiFiInsights(deviceId, range).then(
        (d) => {
          if (cancelled) return;
          setData(d);
          setError(null);
          setLoading(false);
        },
        (e: unknown) => {
          if (cancelled) return;
          setError(e instanceof Error ? e.message : String(e));
          setLoading(false);
        },
      );
    };
    load();
    const timer = setInterval(load, REFRESH_MS);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [deviceId, range, satelliteOf]);

  if (satelliteOf !== undefined) {
    return (
      <div className="panel wifi-insights">
        <h3>Wi-Fi insights</h3>
        <p className="dim" style={{ margin: 0, fontSize: "0.82rem" }}>
          This mesh satellite has no Wi-Fi tree of its own over TR-069. Its clients and traffic are reported by its gateway, {satelliteOf}.
        </p>
      </div>
    );
  }

  const fmtTime = timeFormatter(range);
  const clients = data?.clients ?? [];
  const sampled = Boolean(data?.last_sample_at);

  return (
    <div className="panel wifi-insights">
      <h3>
        Wi-Fi insights
        <span className="wifi-insights-meta">
          {data?.last_sample_at && <span className="dim">last sample {timeAgo(data.last_sample_at)}</span>}
          <span className="seg" role="radiogroup" aria-label="Time window">
            {WINDOWS.map((w) => (
              <button
                key={w.id}
                role="radio"
                aria-checked={range === w.id}
                aria-label={w.label}
                className={range === w.id ? "on" : ""}
                onClick={() => setRange(w.id)}
              >
                {w.short}
              </button>
            ))}
          </span>
        </span>
      </h3>

      {error && <div className="banner error">Could not load Wi-Fi insights: {error}</div>}
      {!data && !error && <div className="loading">Loading Wi-Fi insights…</div>}

      {data && !sampled && (
        <div className="banner info">
          No Wi-Fi samples yet. The ACS reads this gateway's Wi-Fi counters on a check-in about every 5 minutes
          (ACS_WIFI_INSIGHTS_INTERVAL) and builds these charts from the differences. The device keeps no history, so data
          starts from the first sample.
        </div>
      )}

      {data && sampled && (
        <div className={loading ? "wifi-insights-body stale" : "wifi-insights-body"}>
          <ul className="stat-strip wifi-stats" aria-label="Wi-Fi summary">
            {[
              [fmtBytes(data.total_bytes_down), "Downloaded"],
              [fmtBytes(data.total_bytes_up), "Uploaded"],
              [String(clients.filter((c) => c.connected).length), "Connected now"],
              [String(data.client_stats.new), "New clients"],
              [String(data.client_stats.returning), "Returning"],
              [String(data.client_stats.private_mac), "Private MACs"],
              [fmtDurationShort(data.client_stats.average_session_seconds), "Avg session"],
            ].map(([n, l]) => (
              <li className="stat" key={l}>
                <div className="n">{n}</div>
                <div className="l">{l}</div>
              </li>
            ))}
          </ul>

          <div className="wifi-charts">
            <TimeSeriesChart
              title="Bandwidth"
              series={BANDWIDTH_SERIES}
              points={(data.bandwidth ?? []).map((b) => ({ at: b.at, values: { down: b.bps_down, up: b.bps_up } }))}
              format={fmtRate}
              formatTime={fmtTime}
            />
            <TimeSeriesChart
              title="Clients"
              series={CLIENT_SERIES}
              points={(data.client_counts ?? []).map((c) => ({ at: c.at, values: { wireless: c.wireless, wired: c.wired, guest: c.guest } }))}
              format={(n) => String(Math.round(n))}
              formatTime={fmtTime}
            />
          </div>

          <div className="wifi-shares">
            <ShareList
              label="Top SSIDs"
              items={(data.top_ssids ?? []).map((s) => ({ key: String(s.instance), value: s.bytes_down + s.bytes_up }))}
              valueOf={fmtBytes}
              render={(k) => {
                const s = (data.top_ssids ?? []).find((x) => String(x.instance) === k);
                return (
                  <>
                    {s?.name || `SSID ${k}`} <span className="dim">· SSID {k}</span>{" "}
                    <span className={`pill ${s?.role === "guest" ? "pill-warn" : "pill-neutral"}`}>{s?.role}</span>
                  </>
                );
              }}
            />
            <ShareList
              label="Top clients"
              items={(data.top_clients ?? []).map((c) => ({ key: c.mac, value: c.bytes_down + c.bytes_up }))}
              valueOf={fmtBytes}
              render={(k) => {
                const c = (data.top_clients ?? []).find((x) => x.mac === k);
                return (
                  <>
                    {c?.hostname || <span className="mono">{k}</span>}
                    {c?.vendor && <span className="dim"> · {c.vendor}</span>}
                  </>
                );
              }}
            />
            <ShareList
              label="Client vendors"
              items={(data.vendors ?? []).map((v) => ({ key: v.name, value: v.count }))}
              valueOf={String}
              render={(k) => k}
            />
            <ShareList
              label="Client OS (inferred)"
              items={(data.os_inferred ?? []).map((v) => ({ key: v.name, value: v.count }))}
              valueOf={String}
              render={(k) => k}
            />
          </div>

          <h4 className="wifi-clients-title">
            Clients <span className="dim">· {clients.length} seen in this window</span>
          </h4>
          <div className="table-wrap">
            <table aria-label="Wi-Fi clients">
              <thead>
                <tr>
                  <th>Client</th>
                  <th>Network</th>
                  <th>Signal</th>
                  <th>Vendor · OS</th>
                  <th>State</th>
                  <th>Sessions</th>
                </tr>
              </thead>
              <tbody>
                {clients.map((c) => (
                  <tr key={c.mac}>
                    <td>
                      <div>{c.hostname || "—"}</div>
                      <div className="dim">
                        {c.mac}
                        {c.private_mac && <span className="pill pill-neutral wifi-tag">private</span>}
                      </div>
                    </td>
                    <td>
                      <div>{c.wireless ? c.ssid_name || "—" : "Wired"}</div>
                      <div className="dim">
                        {[c.band, c.guest ? "guest" : "", c.mesh_node ? `mesh node ${c.mesh_node}` : ""].filter(Boolean).join(" · ")}
                      </div>
                    </td>
                    <td>{c.signal_dbm == null ? "—" : `${c.signal_dbm} dBm`}</td>
                    <td>
                      <div>{c.vendor || "—"}</div>
                      <div className="dim">{c.os_inferred ? `${c.os_inferred} (inferred)` : ""}</div>
                    </td>
                    <td>
                      {c.connected ? (
                        <span className="pill pill-ok">connected</span>
                      ) : (
                        <span className="dim">seen {timeAgo(c.last_seen_at)}</span>
                      )}
                    </td>
                    <td>
                      {c.sessions} · {fmtDurationShort(c.connected_seconds)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}
