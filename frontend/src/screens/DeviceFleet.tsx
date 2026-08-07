import type { ColumnDef } from "@tanstack/react-table";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Device } from "../api/types";
import { DataTable } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { timeAgo, fmtTime } from "../lib/format";
import { DeviceDetail } from "./DeviceDetail";
import { useLive } from "../lib/useLive";
import { useEscape, useSlashFocus } from "../lib/hotkeys";

export function DeviceFleet() {
  const [devices, setDevices] = useState<Device[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [vendorFilter, setVendorFilter] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async (background = false) => {
    if (!background) setLoading(true);
    if (!background) setError(null);
    try {
      // Single page at the max size: this screen is the "review a working
      // set, drill into one device" view, not the fleet-scale one — Fleet
      // Control (backend-paginated + grouped) is where 1000+ devices go.
      // Virtualized rendering still protects it up to that 500-row cap.
      const res = await api.listDevices(1, 500);
      setDevices(res.items);
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

  const stats = useMemo(() => {
    const online = devices.filter((d) => d.online_status === "ONLINE").length;
    const confirmed = devices.filter((d) => d.connection_request_mode === "DIRECT_IPV4" || d.connection_request_mode === "DIRECT_IPV6").length;
    return { total: devices.length, online, confirmed, vendors: vendors.length };
  }, [devices, vendors]);

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
          <div className="l">online</div>
        </div>
        <div className="stat">
          <div className="n">{stats.confirmed}</div>
          <div className="l">reachability confirmed</div>
        </div>
        <div className="stat">
          <div className="n">{stats.vendors}</div>
          <div className="l">vendors</div>
        </div>
      </div>

      {error && (
        <div className="banner error">
          {error} <button className="btn" style={{ marginLeft: "0.6rem" }} onClick={() => load()}>Retry</button>
        </div>
      )}

      <div className="toolbar">
        <div className="search">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <circle cx="11" cy="11" r="7" />
            <line x1="21" y1="21" x2="16.65" y2="16.65" />
          </svg>
          <input
            ref={searchRef}
            placeholder="Filter by serial, model, manufacturer… (press /)"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <select className="chip" value={vendorFilter} onChange={(e) => setVendorFilter(e.target.value)}>
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
