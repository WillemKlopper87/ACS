import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { FleetHealth } from "../api/types";
import { useLive } from "../lib/useLive";
import { timeAgo } from "../lib/format";

// Build plan §3.3 / design doc v3 §16.1: "the one screen where charts are
// appropriate — inform rate, RPC fault rate, connection request success
// rate, device online/offline/unreachable counts." Grafana already has
// these as time series (build plan §4 Phase 7 dashboards); this is the
// in-app, no-separate-login version — a live snapshot rather than a
// history, which is what an operator actually wants while triaging.
// Deliberately no charting library: every number here renders as a
// proportional bar built from plain CSS, the same "no new dependency"
// discipline the rest of this app follows.
const STATUS_TONE: Record<string, string> = {
  ONLINE: "ok",
  OFFLINE: "danger",
  UNKNOWN: "neutral",
  DIRECT_IPV4: "ok",
  DIRECT_IPV6: "ok",
  PERIODIC_FALLBACK_ONLY: "warn",
  UNREACHABLE: "danger",
  SUCCESS: "ok",
  FAILED: "danger",
  TIMEOUT: "danger",
  QUEUED: "pending",
  RPC_SENT: "pending",
  IN_PROGRESS: "pending",
  AWAITING_TRANSFER_COMPLETE: "pending",
};

const RECENCY_LABEL: Record<string, string> = {
  under_5m: "< 5 min ago",
  under_1h: "< 1 hour ago",
  under_24h: "< 24 hours ago",
  stale: "24h+ ago",
  never: "never informed",
};
const RECENCY_ORDER = ["under_5m", "under_1h", "under_24h", "stale", "never"];

function BarGroup({ title, data, order, labels }: { title: string; data: Record<string, number>; order?: string[]; labels?: Record<string, string> }) {
  const keys = order ? order.filter((k) => k in data) : Object.keys(data);
  const total = Object.values(data).reduce((a, b) => a + b, 0);
  return (
    <div className="panel">
      <h3>{title}</h3>
      {total === 0 ? (
        <p className="dim" style={{ margin: 0 }}>No data yet.</p>
      ) : (
        keys.map((k) => {
          const n = data[k] ?? 0;
          const pct = total > 0 ? (n / total) * 100 : 0;
          const tone = STATUS_TONE[k] ?? "neutral";
          return (
            <div key={k} style={{ marginBottom: "0.55rem" }}>
              <div style={{ display: "flex", justifyContent: "space-between", fontSize: "0.78rem", marginBottom: "0.2rem" }}>
                <span>{labels?.[k] ?? k}</span>
                <span className="dim" style={{ fontFamily: "var(--font-mono)" }}>{n} ({pct.toFixed(0)}%)</span>
              </div>
              <div style={{ height: "0.5rem", borderRadius: "4px", background: "var(--surface-3)", overflow: "hidden" }}>
                <div
                  style={{
                    height: "100%",
                    width: `${pct}%`,
                    background: `var(--${tone === "neutral" ? "ink-faint" : tone})`,
                    transition: "width 0.3s ease",
                  }}
                />
              </div>
            </div>
          );
        })
      )}
    </div>
  );
}

export function FleetHealthScreen() {
  const [health, setHealth] = useState<FleetHealth | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async (background = false) => {
    if (!background) setLoading(true);
    try {
      const res = await api.getFleetHealth();
      setHealth(res);
      setError(null);
    } catch (e) {
      if (!background) setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API — is cmd/api running?");
    } finally {
      if (!background) setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const [live, setLive] = useLive(() => load(true), 8000);

  const deviceTotal = health ? Object.values(health.devices_by_status).reduce((a, b) => a + b, 0) : 0;
  const online = health?.devices_by_status["ONLINE"] ?? 0;

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" onClick={() => load()}>Retry</button>
        </div>
      )}

      <div className="toolbar">
        <div className="toolbar-spacer" />
        {health && <span className="result-count">as of {timeAgo(health.generated_at)}</span>}
        <button
          className={`btn live-toggle ${live ? "on" : ""}`}
          onClick={() => setLive((l) => !l)}
          title={live ? "Auto-refreshing every 8s — click to pause" : "Auto-refresh paused — click to resume"}
        >
          <span className="live-dot" /> {live ? "Live" : "Paused"}
        </button>
        <button className="btn" onClick={() => load()} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
      </div>

      {loading && !health ? (
        <div className="loading">Loading fleet health…</div>
      ) : health ? (
        <>
          <div className="stat-strip">
            <div className="stat">
              <div className="n">{deviceTotal}</div>
              <div className="l">devices</div>
            </div>
            <div className="stat">
              <div className="n accent">{online}</div>
              <div className="l">online now</div>
            </div>
            <div className="stat">
              <div className="n">{health.jobs_last_24h_total}</div>
              <div className="l">jobs, last 24h</div>
            </div>
            <div className="stat">
              <div className="n" style={{ color: health.job_success_rate_pct >= 90 ? "var(--ok)" : health.job_success_rate_pct >= 70 ? "var(--warn)" : "var(--danger)" }}>
                {health.job_success_rate_pct.toFixed(1)}%
              </div>
              <div className="l">job success rate</div>
            </div>
          </div>

          <div className="split two-col">
            <BarGroup title="Devices by online status" data={health.devices_by_status} />
            <BarGroup title="Devices by reachability" data={health.devices_by_reachability} />
            <BarGroup title="Inform recency" data={health.inform_recency} order={RECENCY_ORDER} labels={RECENCY_LABEL} />
            <BarGroup title="Jobs, last 24 hours, by status" data={health.jobs_last_24h} />
          </div>
        </>
      ) : null}
    </section>
  );
}
