import type { ColumnDef } from "@tanstack/react-table";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { Device } from "../api/types";
import { DataTable } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { timeAgo, fmtTime } from "../lib/format";
import { DeviceDetail } from "./DeviceDetail";
import { useLive } from "../lib/useLive";
import { useEscape, useSlashFocus } from "../lib/hotkeys";

// The backend's own maxPageSize (internal/devices/repository.go). This
// screen deliberately loads a single page of it — it's the "review a
// working set, drill into one device" view, with Fleet Control as the
// fleet-scale one — but it has to say so rather than silently presenting
// the first 500 rows as the whole fleet.
const PAGE_SIZE = 500;

export function DeviceFleet() {
  const [devices, setDevices] = useState<Device[]>([]);
  // Total matching rows server-side, which is not devices.length once the
  // fleet exceeds one page.
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [vendorFilter, setVendorFilter] = useState("");
  // The selected device rides in ?device= (audit P2.4: deep links) so a
  // device drill-down can be shared/bookmarked and survives a reload.
  const [searchParams, setSearchParams] = useSearchParams();
  const selectedId = searchParams.get("device");
  const setSelectedId = useCallback(
    (id: string | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (id) next.set("device", id);
          else next.delete("device");
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );
  const searchRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async (background = false) => {
    if (!background) setLoading(true);
    if (!background) setError(null);
    try {
      const res = await api.listDevices(1, PAGE_SIZE);
      setDevices(res.items);
      setTotal(res.total);
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

  const [live, setLive] = useLive(() => load(true), 6000);
  useSlashFocus(searchRef);
  useEscape(() => setSelectedId(null), selectedId !== null);

  const vendors = useMemo(() => [...new Set(devices.map((d) => d.manufacturer))].sort(), [devices]);

  const filtered = useMemo(() => {
    const q = search.toLowerCase();
    return devices.filter(
      (d) =>
        (!q || `${d.oui_serial} ${d.manufacturer} ${d.product_class}`.toLowerCase().includes(q)) &&
        (!vendorFilter || d.manufacturer === vendorFilter),
    );
  }, [devices, search, vendorFilter]);

  // True once the fleet is larger than the single page this screen
  // loads: the counts below then describe the loaded page, not the fleet.
  const truncated = total > devices.length;

  const stats = useMemo(() => {
    const online = devices.filter((d) => d.online_status === "ONLINE").length;
    const confirmed = devices.filter((d) => d.connection_request_mode === "DIRECT_IPV4" || d.connection_request_mode === "DIRECT_IPV6").length;
    return { total: Math.max(total, devices.length), online, confirmed, vendors: vendors.length };
  }, [devices, vendors, total]);

  const columns = useMemo<ColumnDef<Device, any>[]>(
    () => [
      { accessorKey: "oui_serial", header: "Device" },
      {
        id: "vendor",
        header: "Vendor / Model",
        accessorFn: (d) => `${d.manufacturer} ${d.product_class}`,
        cell: ({ row }) => (
          <span className="dim" style={{ fontFamily: "var(--font-ui)" }}>
            {row.original.manufacturer} <span style={{ color: "var(--ink-faint)" }}>·</span> {row.original.product_class}
          </span>
        ),
      },
      {
        accessorKey: "online_status",
        header: "Status",
        cell: ({ getValue }) => <StatusBadge value={getValue() as string} />,
      },
      {
        accessorKey: "connection_request_mode",
        header: "Reachability",
        cell: ({ getValue }) => <StatusBadge value={getValue() as string} />,
      },
      {
        accessorKey: "last_inform_at",
        header: "Last Inform",
        cell: ({ getValue }) => {
          const v = getValue() as string | undefined;
          return (
            <span className="dim" title={v ? `${fmtTime(v)} UTC` : undefined}>
              {timeAgo(v)}
            </span>
          );
        },
      },
    ],
    [],
  );

  return (
    <section>
      <div className="stat-strip">
        <div className="stat">
          <div className="n">{stats.total}</div>
          <div className="l">devices</div>
        </div>
        <div className="stat">
          <div className="n accent">{stats.online}</div>
          <div className="l">{truncated ? "online (loaded)" : "online"}</div>
        </div>
        <div className="stat">
          <div className="n">{stats.confirmed}</div>
          <div className="l">{truncated ? "reachable (loaded)" : "reachability confirmed"}</div>
        </div>
        <div className="stat">
          <div className="n">{stats.vendors}</div>
          <div className="l">vendors</div>
        </div>
      </div>

      {error && (
        <div className="banner error">
          {error} <button className="btn" onClick={() => load()}>Retry</button>
        </div>
      )}

      {truncated && (
        <div className="banner info">
          Showing the first {devices.length.toLocaleString()} of {total.toLocaleString()} devices — the online and
          reachability counts above, the vendor filter and the search below all cover only these. Use Fleet Control for
          fleet-scale work.
        </div>
      )}

      <div className="toolbar">
        <div className="search">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <circle cx="11" cy="11" r="7" />
            <line x1="21" y1="21" x2="16.65" y2="16.65" />
          </svg>
          <input aria-label="Filter by serial, model, manufacturer"
            ref={searchRef}
            placeholder="Filter by serial, model, manufacturer… (press /)"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <select className="chip" aria-label="Filter by vendor" value={vendorFilter} onChange={(e) => setVendorFilter(e.target.value)}>
          <option value="">All vendors</option>
          {vendors.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
        <div className="toolbar-spacer" />
        <button
          className={`btn live-toggle ${live ? "on" : ""}`}
          onClick={() => setLive((l) => !l)}
          title={live ? "Auto-refreshing every 6s — click to pause" : "Auto-refresh paused — click to resume"}
        >
          <span className="live-dot" /> {live ? "Live" : "Paused"}
        </button>
        <button className="btn" onClick={() => load()} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
        <span className="result-count">
          {filtered.length} of {devices.length}
          {truncated ? ` loaded · ${total.toLocaleString()} total` : ""}
        </span>
      </div>

      {loading && devices.length === 0 ? (
        <div className="loading">Loading devices…</div>
      ) : (
        <DataTable
          data={filtered}
          columns={columns}
          getRowId={(d) => d.id}
          selectedRowId={selectedId ?? undefined}
          onRowClick={(d) => setSelectedId(selectedId === d.id ? null : d.id)}
          emptyMessage="No devices match this filter."
          virtualize
          maxHeight="62vh"
        />
      )}

      {selectedId && <DeviceDetail id={selectedId} onClose={() => setSelectedId(null)} />}
    </section>
  );
}
