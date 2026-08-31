import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";
import type { DashboardAlarm, DashboardData, DashboardWidget } from "../api/types";
import { timeAgo } from "../lib/format";
import { toast } from "../lib/toast";

// Customizable fleet dashboard (admin-platform backlog). Charts follow the
// dataviz skill's method: form picked by the data's job (grouped counts →
// a ranked horizontal bar list, the best form for many-category magnitude
// comparison — not a pie/donut; firmware status → a stacked meter, one
// series per state; alarms/temperature → a status list/table, not a
// chart, since neither is a magnitude comparison). Color: sequential
// magnitude bars use one hue (--accent, light→dark isn't needed at this
// value range) per the "sequential = one hue" rule; firmware/alarms reuse
// this app's existing reserved status palette (--ok/--warn/--danger/
// --pending) rather than inventing a second one — no categorical palette
// was needed anywhere here, so the validator doesn't apply.

const GROUP_BY_LABEL: Record<string, string> = { customer: "Customer", region: "Region", project: "Project", manufacturer: "Manufacturer" };
const STATUS_TONE: Record<string, string> = {
  ONLINE: "var(--ok)", OFFLINE: "var(--danger)", UNKNOWN: "var(--ink-faint)",
  DIRECT_IPV4: "var(--ok)", DIRECT_IPV6: "var(--ok)", STUN_ANNEX_G: "var(--warn)",
  PERIODIC_FALLBACK_ONLY: "var(--warn)", UNREACHABLE: "var(--danger)",
};

// RankedBarChart: horizontal bars, sorted descending, one sequential hue.
// Mark spec: ≤24px thick, 4px rounded data-end (square at the baseline),
// value labeled at the tip (outside — never clipped), 2px gap between bars.
function RankedBarChart({ data, toneByKey }: { data: Record<string, number>; toneByKey?: Record<string, string> }) {
  const entries = Object.entries(data).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
  const max = Math.max(...entries.map(([, n]) => n), 1);
  if (entries.length === 0) return <p className="dim" style={{ margin: 0, fontSize: "0.82rem" }}>No data yet.</p>;
  return (
    <div>
      {entries.map(([key, n]) => (
        <div key={key} style={{ marginBottom: "2px" }}>
          <div style={{ display: "flex", justifyContent: "space-between", fontSize: "0.78rem", marginBottom: "0.15rem" }}>
            <span>{key}</span>
            <span className="dim" style={{ fontFamily: "var(--font-mono)" }}>{n}</span>
          </div>
          <div style={{ height: "16px", background: "var(--surface-3)", borderRadius: "4px" }}>
            <div
              style={{
                height: "100%",
                width: `${(n / max) * 100}%`,
                background: toneByKey?.[key] ?? "var(--accent)",
                borderRadius: "4px",
                transition: "width 0.3s ease",
              }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

// Meter: a stacked bar, one segment per state, 2px surface gaps between
// segments, legend below (≥2 series always gets a legend, per spec).
function FirmwareMeter({ upToDate, outdated, unknown }: { upToDate: number; outdated: number; unknown: number }) {
  const total = upToDate + outdated + unknown;
  if (total === 0) return <p className="dim" style={{ margin: 0, fontSize: "0.82rem" }}>No devices with known software versions yet.</p>;
  const segs: [string, number, string][] = [
    ["Up to date", upToDate, "var(--ok)"],
    ["Outdated", outdated, "var(--warn)"],
    ["Unknown", unknown, "var(--ink-faint)"],
  ];
  return (
    <div>
      <div style={{ display: "flex", height: "16px", borderRadius: "4px", overflow: "hidden", gap: "2px" }}>
        {segs.filter(([, n]) => n > 0).map(([label, n, color]) => (
          <div key={label} style={{ width: `${(n / total) * 100}%`, background: color }} title={`${label}: ${n}`} />
        ))}
      </div>
      <div style={{ display: "flex", gap: "1rem", marginTop: "0.5rem", fontSize: "0.76rem" }}>
        {segs.map(([label, n, color]) => (
          <span key={label} style={{ display: "flex", alignItems: "center", gap: "0.35rem" }} className="dim">
            <span style={{ width: "8px", height: "8px", borderRadius: "50%", background: color, flex: "none" }} />
            {label} ({n})
          </span>
        ))}
      </div>
    </div>
  );
}

function AlarmList({ alarms }: { alarms: DashboardAlarm[] }) {
  if (alarms.length === 0) {
    return <p style={{ margin: 0, fontSize: "0.82rem", color: "var(--ok)" }}>✓ No active alarms.</p>;
  }
  return (
    <div>
      {alarms.map((a, i) => (
        <div
          key={i}
          style={{
            display: "flex", gap: "0.6rem", padding: "0.5rem 0.6rem", marginBottom: "0.4rem",
            background: "var(--surface-2)", borderLeft: `3px solid ${a.severity === "critical" ? "var(--danger)" : "var(--warn)"}`,
            borderRadius: "4px",
          }}
        >
          <span style={{ color: a.severity === "critical" ? "var(--danger)" : "var(--warn)", fontSize: "0.9rem", lineHeight: 1.4 }}>
            {a.severity === "critical" ? "⛔" : "⚠"}
          </span>
          <div>
            <div style={{ fontSize: "0.84rem", fontWeight: 600 }}>{a.title}</div>
            <div className="dim" style={{ fontSize: "0.78rem" }}>{a.detail}</div>
          </div>
        </div>
      ))}
    </div>
  );
}

const WIDGET_TITLE: Record<string, string> = {
  status: "Devices by online status",
  reachability: "Devices by reachability",
  inform_recency: "Inform recency",
  alarms: "Active alarms",
  group_by: "Fleet breakdown",
  firmware: "Firmware status",
  temperature: "Temperature",
};

export function Dashboard() {
  const [widgets, setWidgets] = useState<DashboardWidget[]>([]);
  const [groupDim, setGroupDim] = useState<"customer" | "region" | "project" | "manufacturer">("manufacturer");
  const [customizing, setCustomizing] = useState(false);
  const [live, setLive] = useState(true);

  // Server state via TanStack Query (audit P2.4) — this screen is the
  // reference conversion away from the manual load()/useLive pattern:
  // caching, request dedupe, background polling, and error/retry come
  // from the library instead of hand-rolled state.
  const query = useQuery({
    queryKey: ["dashboard"],
    queryFn: async () => {
      const [d, l] = await Promise.all([api.getDashboard(), api.getDashboardLayout()]);
      return { data: d, layout: l.widgets };
    },
    refetchInterval: live ? 10_000 : false,
  });
  const data: DashboardData | null = query.data?.data ?? null;
  const loading = query.isPending || query.isFetching;
  const error = query.error
    ? query.error instanceof ApiError
      ? `${query.error.status}: ${query.error.message}`
      : "Failed to reach the API"
    : null;
  const load = () => query.refetch();

  // The widget list is editable locally (toggle/reorder before Save), so
  // it stays component state, (re)seeded whenever the server copy loads.
  useEffect(() => {
    if (query.data) setWidgets(query.data.layout);
  }, [query.data]);

  function toggleWidget(id: string) {
    setWidgets((prev) => prev.map((w) => (w.id === id ? { ...w, enabled: !w.enabled } : w)));
  }

  function moveWidget(index: number, dir: -1 | 1) {
    setWidgets((prev) => {
      const next = [...prev];
      const target = index + dir;
      if (target < 0 || target >= next.length) return prev;
      [next[index], next[target]] = [next[target], next[index]];
      return next;
    });
  }

  async function saveLayout() {
    try {
      await api.setDashboardLayout(widgets);
      toast("Dashboard layout saved", "success");
      setCustomizing(false);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to save layout", "error");
    }
  }

  const enabled = widgets.filter((w) => w.enabled).map((w) => w.id);

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" style={{ marginLeft: "0.6rem" }} onClick={() => load()}>Retry</button>
        </div>
      )}

      <div className="toolbar">
        <div className="toolbar-spacer" />
        {data && <span className="result-count">as of {timeAgo(data.generated_at)}{data.scoped ? " · scoped to your assigned customers" : ""}</span>}
        <button className="btn" onClick={() => setCustomizing((c) => !c)}>
          {customizing ? "Done" : "Customize"}
        </button>
        <button className={`btn live-toggle ${live ? "on" : ""}`} onClick={() => setLive((l) => !l)}>
          <span className="live-dot" /> {live ? "Live" : "Paused"}
        </button>
        <button className="btn" onClick={() => load()} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
      </div>

      {customizing && (
        <div className="panel" style={{ marginBottom: "1.1rem" }}>
          <h3>Customize widgets</h3>
          <p className="dim" style={{ marginTop: 0, fontSize: "0.8rem" }}>Show/hide and reorder — saved to your account.</p>
          {widgets.map((w, i) => (
            <div key={w.id} className="param-row">
              <span className="path" style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
                <input type="checkbox" checked={w.enabled} onChange={() => toggleWidget(w.id)} />
                {WIDGET_TITLE[w.id] ?? w.id}
              </span>
              <span style={{ display: "flex", gap: "0.3rem" }}>
                <button className="btn" style={{ padding: "0.1em 0.5em" }} onClick={() => moveWidget(i, -1)} disabled={i === 0}>↑</button>
                <button className="btn" style={{ padding: "0.1em 0.5em" }} onClick={() => moveWidget(i, 1)} disabled={i === widgets.length - 1}>↓</button>
              </span>
            </div>
          ))}
          <button className="btn primary" style={{ marginTop: "0.6rem" }} onClick={saveLayout}>Save layout</button>
        </div>
      )}

      {loading && !data ? (
        <div className="loading">Loading dashboard…</div>
      ) : data ? (
        <>
          <div className="stat-strip">
            <div className="stat"><div className="n">{Object.values(data.devices_by_status).reduce((a, b) => a + b, 0)}</div><div className="l">devices</div></div>
            <div className="stat"><div className="n accent">{data.devices_by_status["ONLINE"] ?? 0}</div><div className="l">online now</div></div>
            <div className="stat"><div className="n">{data.jobs_last_24h_total}</div><div className="l">jobs, last 24h</div></div>
            <div className="stat">
              <div className="n" style={{ color: data.job_success_rate_pct >= 90 ? "var(--ok)" : data.job_success_rate_pct >= 70 ? "var(--warn)" : "var(--danger)" }}>
                {data.job_success_rate_pct.toFixed(1)}%
              </div>
              <div className="l">job success rate</div>
            </div>
          </div>

          <div className="split two-col">
            {enabled.includes("alarms") && (
              <div className="panel"><h3>Active alarms</h3><AlarmList alarms={data.alarms} /></div>
            )}

            {enabled.includes("status") && (
              <div className="panel"><h3>Devices by online status</h3><RankedBarChart data={data.devices_by_status} toneByKey={STATUS_TONE} /></div>
            )}

            {enabled.includes("reachability") && (
              <div className="panel"><h3>Devices by reachability</h3><RankedBarChart data={data.devices_by_reachability} toneByKey={STATUS_TONE} /></div>
            )}

            {enabled.includes("inform_recency") && (
              <div className="panel"><h3>Inform recency</h3><RankedBarChart data={data.inform_recency} /></div>
            )}

            {enabled.includes("group_by") && (
              <div className="panel">
                <h3>
                  Fleet breakdown
                  <select className="chip" aria-label="Group devices by" value={groupDim} onChange={(e) => setGroupDim(e.target.value as any)} style={{ fontSize: "0.74rem", padding: "0.2em 0.4em" }}>
                    {Object.entries(GROUP_BY_LABEL).map(([k, label]) => (
                      <option key={k} value={k}>By {label}</option>
                    ))}
                  </select>
                </h3>
                <RankedBarChart data={data.group_by[groupDim]} />
              </div>
            )}

            {enabled.includes("firmware") && (
              <div className="panel">
                <h3>Firmware status</h3>
                <FirmwareMeter upToDate={data.firmware.up_to_date} outdated={data.firmware.outdated} unknown={data.firmware.unknown} />
              </div>
            )}

            {enabled.includes("temperature") && (
              <div className="panel">
                <h3>Temperature</h3>
                {!data.temperature || data.temperature.length === 0 ? (
                  <p className="dim" style={{ margin: 0, fontSize: "0.82rem" }}>
                    No device in scope reports a temperature parameter.
                  </p>
                ) : (
                  data.temperature.map((t, i) => (
                    <div className="param-row" key={i}>
                      <span className="path">{t.DeviceID.slice(0, 8)}… <span className="dim">· {t.ParameterName}</span></span>
                      <span>{t.Value}</span>
                    </div>
                  ))
                )}
              </div>
            )}
          </div>
        </>
      ) : null}
    </section>
  );
}
