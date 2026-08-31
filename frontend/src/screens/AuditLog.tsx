import type { ColumnDef } from "@tanstack/react-table";
import { useEffect, useMemo, useState } from "react";
import { api, ApiError } from "../api/client";
import type { AuditEntry } from "../api/types";
import { DataTable } from "../components/DataTable";
import { fmtTime, timeAgo } from "../lib/format";

export function AuditLog() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [actionFilter, setActionFilter] = useState("");

  async function load() {
    setLoading(true);
    setError(null);
    try {
      const res = await api.listAuditLog();
      setEntries(res.items);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API — is cmd/api running?");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  const actions = useMemo(() => [...new Set(entries.map((e) => e.action))].sort(), [entries]);

  const filtered = useMemo(() => {
    const q = search.toLowerCase();
    return entries.filter(
      (e) =>
        (!q || `${e.actor} ${e.device_id ?? ""} ${JSON.stringify(e.details ?? {})}`.toLowerCase().includes(q)) &&
        (!actionFilter || e.action === actionFilter),
    );
  }, [entries, search, actionFilter]);

  const columns = useMemo<ColumnDef<AuditEntry, any>[]>(
    () => [
      {
        accessorKey: "occurred_at",
        header: "When",
        cell: ({ getValue }) => {
          const v = getValue() as string;
          return <span className="dim" title={`${fmtTime(v)} UTC`}>{timeAgo(v)}</span>;
        },
      },
      { accessorKey: "actor", header: "Actor" },
      { accessorKey: "action", header: "Action" },
      {
        accessorKey: "device_id",
        header: "Device",
        cell: ({ getValue }) => {
          const v = getValue() as string | undefined;
          return <span className="dim">{v ? `${v.slice(0, 8)}…` : "—"}</span>;
        },
      },
      {
        accessorKey: "details",
        header: "Details",
        cell: ({ getValue }) => {
          const v = getValue() as Record<string, unknown> | undefined;
          const s = v ? JSON.stringify(v) : "";
          return (
            <span className="dim ellipsis" title={s}>
              {s || "—"}
            </span>
          );
        },
      },
    ],
    [],
  );

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" style={{ marginLeft: "0.6rem" }} onClick={load}>Retry</button>
        </div>
      )}

      <div className="toolbar">
        <div className="search">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <circle cx="11" cy="11" r="7" />
            <line x1="21" y1="21" x2="16.65" y2="16.65" />
          </svg>
          <input placeholder="Filter by actor, device, details…" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <select className="chip" aria-label="Filter by action" value={actionFilter} onChange={(e) => setActionFilter(e.target.value)}>
          <option value="">All actions</option>
          {actions.map((a) => (
            <option key={a} value={a}>{a}</option>
          ))}
        </select>
        <div className="toolbar-spacer" />
        <button className="btn" onClick={load} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
        <span className="result-count">
          {filtered.length} of {entries.length}
        </span>
      </div>

      {loading && entries.length === 0 ? (
        <div className="loading">Loading audit log…</div>
      ) : (
        <DataTable data={filtered} columns={columns} getRowId={(e) => e.id} emptyMessage="No audit entries match this filter." virtualize maxHeight="70vh" />
      )}
    </section>
  );
}
