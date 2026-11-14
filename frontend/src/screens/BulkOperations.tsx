import type { ColumnDef } from "@tanstack/react-table";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { BulkOperation, BulkOperationDetail, BulkOperationItem } from "../api/types";
import { DataTable } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { useAuth } from "../auth/useAuth";
import { canWrite } from "../auth/roles";
import { fmtTime, timeAgo } from "../lib/format";
import { itemStatus, progressText, sortItems } from "../lib/bulkOperations";
import { toast } from "../lib/toast";
import { useLive } from "../lib/useLive";

// The record of every bulk action and template apply: how far each has
// got, which devices failed and why, and the two things an operator can
// still do about one — cancel what has not been sent, retry what failed.
export function BulkOperations() {
  const [ops, setOps] = useState<BulkOperation[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [detail, setDetail] = useState<BulkOperationDetail | null>(null);
  const [busy, setBusy] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  const selectedId = searchParams.get("id");
  const writable = canWrite(useAuth().role);

  const load = useCallback(
    async (background = false) => {
      if (!background) setLoading(true);
      try {
        const [list, one] = await Promise.all([
          api.listBulkOperations(),
          selectedId ? api.getBulkOperation(selectedId).catch(() => null) : Promise.resolve(null),
        ]);
        setOps(list.items);
        setTotal(list.total);
        setDetail(one);
        setError(null);
      } catch (e) {
        if (!background) setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API");
      } finally {
        if (!background) setLoading(false);
      }
    },
    [selectedId],
  );

  useEffect(() => {
    load();
  }, [load]);
  const [live, setLive] = useLive(() => load(true), 5000);

  const select = (id: string | null) => setSearchParams(id ? { id } : {});

  const change = async (action: "cancel" | "retry") => {
    if (!detail) return;
    const op = detail.operation;
    if (action === "cancel" && !window.confirm(`Cancel the ${op.queued} job${op.queued === 1 ? "" : "s"} still queued for "${op.label}"? Jobs already sent to a device will finish.`)) return;
    setBusy(true);
    try {
      if (action === "cancel") {
        const res = await api.cancelBulkOperation(op.id);
        setDetail(res);
        toast(`Cancelled ${res.cancelled ?? 0} queued job${res.cancelled === 1 ? "" : "s"}`, "success");
      } else {
        const res = await api.retryFailedBulkOperation(op.id);
        setDetail(res);
        const skipped = Object.entries(res.skipped ?? {});
        toast(`Queued ${res.retried ?? 0} again`, "success");
        // Say why the rest were left: the reason is the server's own.
        for (const [reason, n] of skipped) toast(`${n} not retried: ${reason}`, "info");
      }
      load(true);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : `Could not ${action} the operation`, "error");
    } finally {
      setBusy(false);
    }
  };

  const columns = useMemo<ColumnDef<BulkOperation, any>[]>(
    () => [
      { accessorKey: "label", header: "Operation" },
      { accessorKey: "state", header: "State", cell: ({ getValue }) => <StatusBadge value={getValue() as string} /> },
      { id: "progress", header: "Progress", accessorFn: (o) => o.succeeded, cell: ({ row }) => <span className="dim">{progressText(row.original)}</span> },
      {
        id: "failed",
        header: "Failed",
        accessorFn: (o) => o.failed + o.not_queued,
        cell: ({ getValue }) => <span className={(getValue() as number) > 0 ? "" : "dim"}>{getValue() as number}</span>,
      },
      { accessorKey: "created_by", header: "By", cell: ({ getValue }) => <span className="dim">{getValue() as string}</span> },
      {
        accessorKey: "created_at",
        header: "Started",
        cell: ({ getValue }) => (
          <span className="dim" title={`${fmtTime(getValue() as string)} UTC`}>
            {timeAgo(getValue() as string)}
          </span>
        ),
      },
    ],
    [],
  );

  const itemColumns = useMemo<ColumnDef<BulkOperationItem, any>[]>(
    () => [
      {
        id: "device",
        header: "Device",
        accessorFn: (it) => it.device_label || it.oui_serial || it.device_id,
        cell: ({ row }) => {
          const it = row.original;
          const name = it.device_label || it.oui_serial || it.device_id;
          // An ID that matched no device has nothing to link to.
          return it.oui_serial ? <Link to={`/devices/${it.device_id}`}>{name}</Link> : <span className="dim">{name}</span>;
        },
      },
      { id: "status", header: "Outcome", accessorFn: itemStatus, cell: ({ getValue }) => <StatusBadge value={getValue() as string} /> },
      {
        id: "detail",
        header: "Detail",
        accessorFn: (it) => it.error || it.fault_string || it.detail || "",
        cell: ({ getValue }) => (
          <span className="dim ellipsis" title={getValue() as string}>
            {(getValue() as string) || "—"}
          </span>
        ),
      },
      { accessorKey: "command_key", header: "Job", cell: ({ getValue }) => <span className="dim">{(getValue() as string) || "—"}</span> },
      { accessorKey: "retries", header: "Retries", cell: ({ getValue }) => <span className="dim">{(getValue() as number) || "—"}</span> },
    ],
    [],
  );

  const op = detail?.operation;
  const retryable = detail ? detail.items.filter((it) => it.job_status === "FAILED" || it.job_status === "TIMEOUT").length : 0;

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" onClick={() => load()}>Retry</button>
        </div>
      )}

      {op && detail && (
        <div className="panel" style={{ marginBottom: "0.9rem" }}>
          <h3>
            {op.label} <StatusBadge value={op.state} />
            <button className="close-detail" aria-label="Close operation" onClick={() => select(null)}>
              ✕
            </button>
          </h3>
          <p className="dim" style={{ marginTop: 0 }}>
            Started {timeAgo(op.created_at)} by {op.created_by} · {progressText(op)}
          </p>
          <div className="stat-strip">
            <div className="stat"><div className="n accent">{op.succeeded}</div><div className="l">succeeded</div></div>
            <div className="stat"><div className="n">{op.queued + op.running}</div><div className="l">queued / in flight</div></div>
            <div className="stat"><div className="n">{op.failed}</div><div className="l">failed / timed out</div></div>
            <div className="stat"><div className="n">{op.cancelled}</div><div className="l">cancelled</div></div>
            <div className="stat"><div className="n">{op.not_queued}</div><div className="l">not queued</div></div>
          </div>
          <div className="form-row">
            <button className="btn" disabled={!writable || busy || op.queued === 0} onClick={() => change("cancel")}
              title={op.queued === 0 ? "Nothing is still queued" : "Withdraw the jobs not yet sent to a device"}>
              Cancel queued ({op.queued})
            </button>
            <button className="btn primary" disabled={!writable || busy || retryable === 0} onClick={() => change("retry")}
              title={retryable === 0 ? "No job has failed or timed out" : "Queue a new job for each device whose job failed or timed out; cancelled jobs are left alone"}>
              Retry failed ({retryable})
            </button>
            {op.not_queued > 0 && (
              <span className="dim" style={{ fontSize: "0.8rem" }}>
                Devices that were not queued are not retried: fix the reason shown, then run the action again for them.
              </span>
            )}
          </div>
          <DataTable data={sortItems(detail.items)} columns={itemColumns} getRowId={(it) => it.device_id} emptyMessage="No devices in this operation." />
        </div>
      )}
      {selectedId && !detail && !loading && <div className="banner error">That operation was not found, or none of its devices is yours to see.</div>}

      <div className="toolbar">
        <span className="dim">Every bulk action and template apply is recorded here.</span>
        <div className="toolbar-spacer" />
        <button className={`btn live-toggle ${live ? "on" : ""}`} onClick={() => setLive((l) => !l)}
          title={live ? "Auto-refreshing every 5s — click to pause" : "Auto-refresh paused — click to resume"}>
          <span className="live-dot" /> {live ? "Live" : "Paused"}
        </button>
        <button className="btn" onClick={() => load()} disabled={loading}>{loading ? "Loading…" : "Refresh"}</button>
        <span className="result-count">{ops.length} of {total}</span>
      </div>

      {loading && ops.length === 0 ? (
        <div className="loading">Loading operations…</div>
      ) : (
        <DataTable
          data={ops}
          columns={columns}
          getRowId={(o) => o.id}
          onRowClick={(o) => select(o.id)}
          selectedRowId={selectedId ?? undefined}
          emptyMessage="No bulk operations yet. Run one from Bulk actions or apply a template."
        />
      )}
    </section>
  );
}
