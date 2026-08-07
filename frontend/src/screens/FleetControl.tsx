import type { ColumnDef } from "@tanstack/react-table";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { BulkActionResponse, ConfigTemplate, Device, DeviceGroupCount } from "../api/types";
import { DataTable } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { timeAgo } from "../lib/format";
import { useAuth } from "../auth/AuthContext";
import { canWrite } from "../auth/roles";
import { useLive } from "../lib/useLive";
import { useSlashFocus } from "../lib/hotkeys";
import { toast } from "../lib/toast";

type BulkActionType = "CONNECTION_REQUEST" | "REFRESH_CELLULAR" | "SET_PARAMETER" | "APPLY_TEMPLATE";

const PAGE_SIZES = [50, 100, 250, 500];

// Fleet Control: the mass-review/mass-control counterpart to Device Fleet.
// Where Device Fleet is built for "here are my devices, let me inspect
// one," this is built for "here are my 150 Huawei units, let me act on
// 40 of them at once" — grouped summary computed in SQL (cheap regardless
// of fleet size), a dense virtualized+paginated table, and a bulk-action
// toolbar. Selection accumulates across page changes; "Select all N
// matching" (below) closes the gap this screen used to have for selecting
// beyond the current page without paging through it by hand — it resolves
// the same group-chip/search filter server-side via
// GET /api/v1/devices/ids, capped at 5000 matches.
export function FleetControl() {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [groups, setGroups] = useState<DeviceGroupCount[]>([]);
  const [devices, setDevices] = useState<Device[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(100);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeGroup, setActiveGroup] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());

  const [actionType, setActionType] = useState<BulkActionType>("CONNECTION_REQUEST");
  const [ssidValue, setSsidValue] = useState("");
  const [templates, setTemplates] = useState<ConfigTemplate[]>([]);
  const [templateId, setTemplateId] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<BulkActionResponse | null>(null);
  const busyRef = useRef(busy);
  useEffect(() => {
    busyRef.current = busy;
  }, [busy]);
  const searchRef = useRef<HTMLInputElement>(null);

  async function loadGroups() {
    try {
      const res = await api.getDevicesSummary();
      setGroups(res.groups);
    } catch {
      // Non-fatal — the device table below still works without the summary strip.
    }
  }

  const loadPage = useCallback(
    async (background = false) => {
      if (background && busyRef.current) return; // don't clobber an in-flight bulk action
      if (!background) setLoading(true);
      try {
        const res = await api.listDevices(page, pageSize);
        setDevices(res.items);
        setTotal(res.total);
        setError(null);
      } catch (e) {
        if (!background) setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API — is cmd/api running?");
      } finally {
        if (!background) setLoading(false);
      }
    },
    [page, pageSize],
  );

  useEffect(() => {
    loadGroups();
    api.listTemplates().then((res) => setTemplates(res.items)).catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    loadPage();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, pageSize]);

  const [live, setLive] = useLive(() => {
    loadPage(true);
    loadGroups();
  }, 6000);
  useSlashFocus(searchRef);

  const groupKey = (g: DeviceGroupCount) => `${g.Manufacturer}|${g.OnlineStatus}|${g.ConnectionRequestMode}`;
  const deviceGroupKey = (d: Device) => `${d.manufacturer}|${d.online_status}|${d.connection_request_mode}`;

  const visible = useMemo(() => {
    const q = search.toLowerCase();
    return devices.filter(
      (d) =>
        (!activeGroup || deviceGroupKey(d) === activeGroup) &&
        (!q || `${d.oui_serial} ${d.manufacturer} ${d.product_class}`.toLowerCase().includes(q)),
    );
  }, [devices, activeGroup, search]);

  // Server-side equivalent of the group-chip/search filter above, resolved
  // through GET /api/v1/devices/ids so "select all" can reach beyond the
  // current page without the operator paging through it by hand.
  const matchFilter = useMemo(() => {
    const [manufacturer, onlineStatus, connMode] = activeGroup ? activeGroup.split("|") : ["", "", ""];
    return { manufacturer, online_status: onlineStatus, connection_request_mode: connMode, search };
  }, [activeGroup, search]);
  const hasActiveFilter = Boolean(activeGroup || search.trim());
  const [matchCount, setMatchCount] = useState<number | null>(null);
  const [selectingAll, setSelectingAll] = useState(false);

  useEffect(() => {
    if (!hasActiveFilter) {
      setMatchCount(null);
      return;
    }
    let cancelled = false;
    api
      .matchingDeviceIds(matchFilter)
      .then((res) => {
        if (!cancelled) setMatchCount(res.count);
      })
      .catch(() => {
        if (!cancelled) setMatchCount(null);
      });
    return () => {
      cancelled = true;
    };
  }, [matchFilter, hasActiveFilter]);

  async function selectAllMatching() {
    setSelectingAll(true);
    try {
      const res = await api.matchingDeviceIds(matchFilter);
      setSelectedIds(new Set(res.ids));
      toast(`Selected ${res.count} device${res.count === 1 ? "" : "s"} matching this filter`, "success");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to select all matching devices", "error");
    } finally {
      setSelectingAll(false);
    }
  }

  function toggle(id: string) {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });
  }

  function toggleAll(checked: boolean) {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      for (const d of visible) {
        checked ? next.add(d.id) : next.delete(d.id);
      }
      return next;
    });
  }

  async function applyBulkAction() {
    if (selectedIds.size === 0) return;
    setBusy(true);
    setResult(null);
    try {
      const ids = [...selectedIds];
      if (actionType === "APPLY_TEMPLATE") {
        const t = templates.find((tpl) => tpl.id === templateId);
        const res = await api.applyTemplate(templateId, { device_ids: ids });
        setSelectedIds(new Set());
        toast(
          `Applied "${t?.name ?? templateId}": ${res.succeeded} of ${res.requested} devices queued successfully`,
          res.succeeded === res.requested ? "success" : "info",
        );
        loadGroups();
        return;
      }
      const res =
        actionType === "SET_PARAMETER"
          ? await api.bulkAction(ids, "SET_PARAMETER", {
              parameters: [{ name: "Device.WiFi.SSID.1.SSID", value: ssidValue, type: "string" }],
            })
          : await api.bulkAction(ids, actionType, { timeoutSeconds: 30 });
      setResult(res);
      setSelectedIds(new Set());
      toast(`${res.action}: ${res.succeeded} of ${res.requested} queued successfully`, res.succeeded === res.requested ? "success" : "info");
      loadGroups();
    } catch (e) {
      const msg = e instanceof ApiError ? `${e.status}: ${e.message}` : "Bulk action failed";
      setError(msg);
      toast(msg, "error");
    } finally {
      setBusy(false);
    }
  }

  const columns = useMemo<ColumnDef<Device, any>[]>(
    () => [
      { accessorKey: "oui_serial", header: "Device" },
      {
        id: "vendor",
        header: "Vendor / Model",
        accessorFn: (d) => `${d.manufacturer} ${d.product_class}`,
        cell: ({ row }) => (
          <span className="dim" style={{ fontFamily: "var(--font-ui)" }}>
            {row.original.manufacturer} · {row.original.product_class}
          </span>
        ),
      },
      { accessorKey: "online_status", header: "Status", cell: ({ getValue }) => <StatusBadge value={getValue() as string} /> },
      {
        accessorKey: "connection_request_mode",
        header: "Reachability",
        cell: ({ getValue }) => <StatusBadge value={getValue() as string} />,
      },
      {
        accessorKey: "last_inform_at",
        header: "Last Inform",
        cell: ({ getValue }) => <span className="dim">{timeAgo(getValue() as string)}</span>,
      },
    ],
    [],
  );

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const allOnPageSelected = visible.length > 0 && visible.every((d) => selectedIds.has(d.id));

  return (
    <section>
      <div className="stat-strip">
        <div className="stat">
          <div className="n">{total}</div>
          <div className="l">devices (fleet-wide)</div>
        </div>
        <div className="stat">
          <div className="n accent">{selectedIds.size}</div>
          <div className="l">selected</div>
        </div>
        <div className="stat">
          <div className="n">
            {page}/{totalPages}
          </div>
          <div className="l">page</div>
        </div>
      </div>

      {error && (
        <div className="banner error">
          {error} <button className="btn" style={{ marginLeft: "0.6rem" }} onClick={() => loadPage()}>Retry</button>
        </div>
      )}

      {/* Grouped summary — SQL GROUP BY, cheap at any fleet size. Click a
          chip to filter the current page to that group. */}
      <div className="toolbar" style={{ flexWrap: "wrap" }}>
        {groups.map((g) => {
          const key = groupKey(g);
          return (
            <button
              key={key}
              className="btn"
              style={activeGroup === key ? { borderColor: "var(--accent)", color: "var(--accent)" } : undefined}
              onClick={() => setActiveGroup(activeGroup === key ? null : key)}
            >
              {g.Manufacturer} <StatusBadge value={g.OnlineStatus} /> <StatusBadge value={g.ConnectionRequestMode} />
              <span className="dim">({g.Count})</span>
            </button>
          );
        })}
        {activeGroup && (
          <button className="btn" onClick={() => setActiveGroup(null)}>
            Clear group filter ✕
          </button>
        )}
      </div>

      <div className="toolbar">
        <div className="search">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <circle cx="11" cy="11" r="7" />
            <line x1="21" y1="21" x2="16.65" y2="16.65" />
          </svg>
          <input ref={searchRef} placeholder="Filter this page… (press /)" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        {hasActiveFilter && matchCount !== null && (
          <button className="btn" disabled={selectingAll || matchCount === 0} onClick={selectAllMatching}>
            {selectingAll ? "Selecting…" : `Select all ${matchCount} matching`}
          </button>
        )}
        <select className="chip" value={pageSize} onChange={(e) => { setPageSize(Number(e.target.value)); setPage(1); }}>
          {PAGE_SIZES.map((s) => (
            <option key={s} value={s}>{s} / page</option>
          ))}
        </select>
        <button className="btn" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>← Prev</button>
        <button className="btn" disabled={page >= totalPages} onClick={() => setPage((p) => p + 1)}>Next →</button>
        <div className="toolbar-spacer" />
        <button
          className={`btn live-toggle ${live ? "on" : ""}`}
          onClick={() => setLive((l) => !l)}
          title={live ? "Auto-refreshing every 6s — click to pause" : "Auto-refresh paused — click to resume"}
        >
          <span className="live-dot" /> {live ? "Live" : "Paused"}
        </button>
        <button className="btn" onClick={() => loadPage()} disabled={loading}>{loading ? "Loading…" : "Refresh"}</button>
        <span className="result-count">{visible.length} on this page</span>
      </div>

      {loading && devices.length === 0 ? (
        <div className="loading">Loading devices…</div>
      ) : (
        <DataTable
          data={visible}
          columns={columns}
          getRowId={(d) => d.id}
          emptyMessage="No devices on this page match the filter."
          virtualize
          rowHeight={30}
          maxHeight="48vh"
          selection={{ selectedIds, onToggle: toggle, onToggleAll: toggleAll }}
        />
      )}

      {selectedIds.size > 0 && (
        <div className="panel" style={{ marginTop: "1rem" }}>
          <h3>
            Bulk action — {selectedIds.size} device{selectedIds.size === 1 ? "" : "s"} selected
            {allOnPageSelected && (
              <span className="dim" style={{ fontWeight: 400, fontSize: "0.78rem" }}>
                (selection persists across pages — page through and it accumulates)
              </span>
            )}
          </h3>
          <div style={{ display: "flex", gap: "0.6rem", alignItems: "center", flexWrap: "wrap" }}>
            <select className="chip" value={actionType} onChange={(e) => setActionType(e.target.value as BulkActionType)}>
              <option value="CONNECTION_REQUEST">Queue Connection Request</option>
              <option value="REFRESH_CELLULAR">Refresh cellular diagnostics</option>
              <option value="SET_PARAMETER">Set WiFi SSID</option>
              <option value="APPLY_TEMPLATE">Apply config template</option>
            </select>
            {actionType === "SET_PARAMETER" && (
              <input
                placeholder="New SSID…"
                value={ssidValue}
                onChange={(e) => setSsidValue(e.target.value)}
                style={{ background: "var(--surface-2)", border: "1px solid var(--border)", borderRadius: 6, padding: "0.4rem 0.6rem", color: "var(--ink)", fontFamily: "var(--font-mono)", fontSize: "0.82rem" }}
              />
            )}
            {actionType === "APPLY_TEMPLATE" && (
              <select className="chip" value={templateId} onChange={(e) => setTemplateId(e.target.value)}>
                <option value="">
                  {templates.length === 0 ? "No templates yet — create one on the Templates screen" : "Select a template…"}
                </option>
                {templates.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name} ({t.parameters.length} params)
                  </option>
                ))}
              </select>
            )}
            <button
              className="btn primary"
              disabled={
                busy ||
                !writable ||
                (actionType === "SET_PARAMETER" && !ssidValue.trim()) ||
                (actionType === "APPLY_TEMPLATE" && !templateId)
              }
              title={writable ? undefined : "Read-only role — bulk actions require operator or admin"}
              onClick={applyBulkAction}
            >
              {busy ? "Applying…" : `Apply to ${selectedIds.size} device${selectedIds.size === 1 ? "" : "s"}`}
            </button>
            <button className="btn" onClick={() => setSelectedIds(new Set())}>
              Clear selection
            </button>
          </div>
        </div>
      )}

      {result && (
        <div className="banner info" style={{ marginTop: "0.9rem" }}>
          <b>{result.action}</b>: {result.succeeded} of {result.requested} queued successfully.
          {result.results.some((r) => r.error) && (
            <div style={{ marginTop: "0.4rem", fontFamily: "var(--font-mono)", fontSize: "0.78rem" }}>
              {result.results
                .filter((r) => r.error)
                .slice(0, 5)
                .map((r) => (
                  <div key={r.device_id}>
                    {r.device_id.slice(0, 8)}… — {r.error}
                  </div>
                ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}
