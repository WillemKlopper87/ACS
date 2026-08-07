import type { ColumnDef } from "@tanstack/react-table";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Job } from "../api/types";
import { DataTable } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { durationSeconds, fmtTime, timeAgo } from "../lib/format";
import { useLive } from "../lib/useLive";
import { useSlashFocus } from "../lib/hotkeys";

export function Jobs() {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async (background = false) => {
    if (!background) setLoading(true);
    try {
      const res = await api.listJobs();
      setJobs(res.items);
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

  const [live, setLive] = useLive(() => load(true), 5000);
  useSlashFocus(searchRef);

  const types = useMemo(() => [...new Set(jobs.map((j) => j.type))].sort(), [jobs]);
  const statuses = useMemo(() => [...new Set(jobs.map((j) => j.status))], [jobs]);

  const filtered = useMemo(() => {
    const q = search.toLowerCase();
    return jobs.filter(
      (j) =>
        (!q || `${j.command_key} ${j.device_id} ${j.fault_string ?? ""}`.toLowerCase().includes(q)) &&
        (!typeFilter || j.type === typeFilter) &&
        (!statusFilter || j.status === statusFilter),
    );
  }, [jobs, search, typeFilter, statusFilter]);

  const stats = useMemo(() => {
    const success = jobs.filter((j) => j.status === "SUCCESS").length;
    const failed = jobs.filter((j) => j.status === "FAILED" || j.status === "TIMEOUT").length;
    const queued = jobs.filter((j) => j.status === "QUEUED" || j.status === "IN_PROGRESS" || j.status === "RPC_SENT").length;
    return { total: jobs.length, success, failed, queued };
  }, [jobs]);

  const columns = useMemo<ColumnDef<Job, any>[]>(
    () => [
      { accessorKey: "command_key", header: "Command Key" },
      {
        accessorKey: "device_id",
        header: "Device",
        cell: ({ getValue }) => <span className="dim">{(getValue() as string).slice(0, 8)}…</span>,
      },
      { accessorKey: "type", header: "Type", cell: ({ getValue }) => <span className="dim">{getValue() as string}</span> },
      { accessorKey: "status", header: "Status", cell: ({ getValue }) => <StatusBadge value={getValue() as string} /> },
      {
        accessorKey: "created_at",
        header: "Created",
        cell: ({ getValue }) => {
          const v = getValue() as string;
          return (
            <span className="dim" title={`${fmtTime(v)} UTC`}>
              {timeAgo(v)}
            </span>
          );
        },
      },
      {
        id: "duration",
        header: "Duration",
        accessorFn: (j) => durationSeconds(j.created_at, j.completed_at) ?? -1,
        cell: ({ row }) => {
          const d = durationSeconds(row.original.created_at, row.original.completed_at);
          return <span className="dim">{d != null ? `${d}s` : "—"}</span>;
        },
      },
      {
        accessorKey: "fault_string",
        header: "Detail",
        cell: ({ getValue }) => (
          <span className="dim ellipsis" title={(getValue() as string) ?? ""}>
            {(getValue() as string) || "—"}
          </span>
        ),
      },
    ],
    [],
  );

  return (
    <section>
      <div className="stat-strip">
        <div className="stat">
          <div className="n">{stats.total}</div>
          <div className="l">jobs</div>
        </div>
        <div className="stat">
          <div className="n accent">{stats.success}</div>
          <div className="l">succeeded</div>
        </div>
        <div className="stat">
          <div className="n">{stats.failed}</div>
          <div className="l">failed / timed out</div>
        </div>
        <div className="stat">
          <div className="n">{stats.queued}</div>
          <div className="l">queued</div>
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
          <input ref={searchRef} placeholder="Filter by command_key, device, fault… (press /)" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <select className="chip" value={typeFilter} onChange={(e) => setTypeFilter(e.target.value)}>
          <option value="">All types</option>
          {types.map((t) => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
        <select className="chip" value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
          <option value="">All statuses</option>
          {statuses.map((s) => (
            <option key={s} value={s}>{s}</option>
          ))}
        </select>
        <div className="toolbar-spacer" />
        <button
          className={`btn live-toggle ${live ? "on" : ""}`}
          onClick={() => setLive((l) => !l)}
          title={live ? "Auto-refreshing every 5s — click to pause" : "Auto-refresh paused — click to resume"}
        >
          <span className="live-dot" /> {live ? "Live" : "Paused"}
        </button>
        <button className="btn" onClick={() => load()} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
        <span className="result-count">
          {filtered.length} of {jobs.length}
        </span>
      </div>

      {loading && jobs.length === 0 ? (
        <div className="loading">Loading jobs…</div>
      ) : (
        <DataTable
          data={filtered}
          columns={columns}
          getRowId={(j) => j.command_key}
          emptyMessage="No jobs match this filter."
        />
      )}
    </section>
  );
}
