import type { ColumnDef } from "@tanstack/react-table";
import { useEffect, useMemo, useState } from "react";
import { api, ApiError } from "../api/client";
import type { ScheduledJob } from "../api/types";
import { DataTable } from "../components/DataTable";
import { fmtTime, timeAgo } from "../lib/format";
import { useAuth } from "../auth/useAuth";
import { canWrite } from "../auth/roles";
import { toast } from "../lib/toast";
import { useCustomers, customerName } from "../lib/useCustomers";

const JOB_TYPES = ["GET_PARAMETER", "SET_PARAMETER", "DIAGNOSTICS_PING", "CONNECTION_REQUEST"];

export function ScheduledJobs() {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [items, setItems] = useState<ScheduledJob[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [jobType, setJobType] = useState(JOB_TYPES[0]);
  const [targetType, setTargetType] = useState<"DEVICE" | "GROUP">("DEVICE");
  const [targetId, setTargetId] = useState("");
  const [intervalSeconds, setIntervalSeconds] = useState(300);
  const [paramPaths, setParamPaths] = useState("Device.DeviceInfo.SoftwareVersion");
  const [customerId, setCustomerId] = useState("");
  const [createError, setCreateError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const customers = useCustomers();

  async function load() {
    setLoading(true);
    setError(null);
    try {
      const res = await api.listScheduledJobs();
      setItems(res.items);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API — is cmd/api running?");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    setCreateError(null);
    setCreating(true);
    try {
      const payload =
        jobType === "GET_PARAMETER"
          ? { paths: paramPaths.split(",").map((s) => s.trim()).filter(Boolean) }
          : jobType === "DIAGNOSTICS_PING"
            ? { host: paramPaths.trim() || "8.8.8.8" }
            : {};
      await api.createScheduledJob({
        name,
        job_type: jobType,
        target_type: targetType,
        target_id: targetId,
        payload,
        interval_seconds: intervalSeconds,
        customer_id: customerId || null,
      });
      toast(`Schedule "${name}" created`, "success");
      setName("");
      setTargetId("");
      setCustomerId("");
      await load();
    } catch (e) {
      setCreateError(e instanceof ApiError ? e.message : "Failed to create schedule");
    } finally {
      setCreating(false);
    }
  }

  async function onToggle(sj: ScheduledJob) {
    try {
      await api.setScheduledJobEnabled(sj.id, !sj.enabled);
      toast(`"${sj.name}" ${sj.enabled ? "disabled" : "enabled"}`, "info");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to update schedule", "error");
    }
  }

  async function onDelete(id: string) {
    try {
      await api.deleteScheduledJob(id);
      toast("Schedule deleted", "info");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to delete schedule", "error");
    }
  }

  const columns = useMemo<ColumnDef<ScheduledJob, any>[]>(
    () => [
      { accessorKey: "name", header: "Name" },
      { accessorKey: "customer_id", header: "Customer", cell: ({ getValue }) => <span className="dim">{customerName(customers, getValue() as string | null)}</span> },
      { accessorKey: "job_type", header: "Type", cell: ({ getValue }) => <span className="dim">{getValue() as string}</span> },
      { accessorKey: "target_type", header: "Target", cell: ({ row }) => <span className="dim">{row.original.target_type}: {row.original.target_id.slice(0, 8)}…</span> },
      { accessorKey: "interval_seconds", header: "Interval", cell: ({ getValue }) => <span className="dim">{getValue() as number}s</span> },
      {
        accessorKey: "enabled",
        header: "Status",
        cell: ({ row }) => (
          <span className={`pill ${row.original.enabled ? "pill-ok" : "pill-neutral"}`}>{row.original.enabled ? "enabled" : "disabled"}</span>
        ),
      },
      {
        accessorKey: "next_run_at",
        header: "Next Run",
        cell: ({ getValue }) => {
          const v = getValue() as string;
          return <span className="dim" title={`${fmtTime(v)} UTC`}>{timeAgo(v)}</span>;
        },
      },
      {
        accessorKey: "last_run_at",
        header: "Last Run",
        cell: ({ getValue }) => {
          const v = getValue() as string | undefined;
          return <span className="dim">{v ? timeAgo(v) : "never"}</span>;
        },
      },
      {
        id: "actions",
        header: "",
        cell: ({ row }) => (
          <div style={{ display: "flex", gap: "0.4rem" }}>
            <button className="btn" disabled={!writable} onClick={() => onToggle(row.original)}>
              {row.original.enabled ? "Disable" : "Enable"}
            </button>
            <button className="btn" disabled={!writable} onClick={() => onDelete(row.original.id)}>
              Delete
            </button>
          </div>
        ),
      },
    ],
    [writable, customers],
  );

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" onClick={load}>Retry</button>
        </div>
      )}

      <div className="panel">
        <h3>Create scheduled job</h3>
        <form onSubmit={onCreate}>
          <div className="form-row">
            <input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} required />
            <select className="chip" aria-label="Job type" value={jobType} onChange={(e) => setJobType(e.target.value)}>
              {JOB_TYPES.map((t) => (
                <option key={t} value={t}>{t}</option>
              ))}
            </select>
          </div>
          <div className="form-row">
            <select className="chip" aria-label="Target type" value={targetType} onChange={(e) => setTargetType(e.target.value as "DEVICE" | "GROUP")}>
              <option value="DEVICE">Device</option>
              <option value="GROUP">Group</option>
            </select>
            <input placeholder="Target device/group ID" value={targetId} onChange={(e) => setTargetId(e.target.value)} required />
          </div>
          <div className="form-row">
            <input
              placeholder={jobType === "GET_PARAMETER" ? "Comma-separated parameter paths" : jobType === "DIAGNOSTICS_PING" ? "Ping host" : "(no payload needed)"}
              value={paramPaths}
              onChange={(e) => setParamPaths(e.target.value)}
              disabled={jobType !== "GET_PARAMETER" && jobType !== "DIAGNOSTICS_PING"}
            />
            <input
              type="number"
              min={60}
              placeholder="Interval (seconds)"
              value={intervalSeconds}
              onChange={(e) => setIntervalSeconds(Number(e.target.value))}
              style={{ maxWidth: "10rem" }}
            />
          </div>
          <div className="form-row">
            <select className="chip" aria-label="Customer" value={customerId} onChange={(e) => setCustomerId(e.target.value)}>
              <option value="">Platform-wide (no customer)</option>
              {customers.map((c) => (
                <option key={c.id} value={c.id}>{c.name}</option>
              ))}
            </select>
          </div>
          {createError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{createError}</div>}
          <div className="form-row">
            <button type="submit" className="btn primary" disabled={creating || !writable || !name || !targetId}>
              {creating ? "Creating…" : "Create schedule"}
            </button>
          </div>
        </form>
      </div>

      {loading && items.length === 0 ? (
        <div className="loading">Loading scheduled jobs…</div>
      ) : (
        <div style={{ marginTop: "1.1rem" }}>
          <DataTable data={items} columns={columns} getRowId={(sj) => sj.id} emptyMessage="No scheduled jobs yet." />
        </div>
      )}
    </section>
  );
}
