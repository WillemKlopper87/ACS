import type { ColumnDef } from "@tanstack/react-table";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { AlertIncident, AlertPolicy, DeviceGroup } from "../api/types";
import { DataTable } from "../components/DataTable";
import { Drawer } from "../components/Drawer";
import { useAuth } from "../auth/useAuth";
import { canManage } from "../auth/roles";
import { confirmDialog } from "../lib/confirm";
import { fmtSeconds, policyThresholdSeconds } from "../lib/alerting";
import { timeAgo } from "../lib/format";
import { toast } from "../lib/toast";
import { useLive } from "../lib/useLive";

type DraftStep = { after: string; destination: string; recipient: string };
const emptyStep = (): DraftStep => ({ after: "900", destination: "nms", recipient: "" });

const SCOPE_LABEL: Record<AlertPolicy["scope"], string> = { fleet: "Whole fleet", tenant: "Tenant", group: "Device group", device: "One device" };

// Open incidents first, because that is what someone opens this screen
// for; then the policies that raise them. Creating a policy is a button.
export function Alerting() {
  // Alert policies are a curated permission that managers hold by default
  // (policy.manage); the API remains the check.
  const writable = canManage(useAuth().role);
  const [policies, setPolicies] = useState<AlertPolicy[]>([]);
  const [incidents, setIncidents] = useState<AlertIncident[]>([]);
  const [groups, setGroups] = useState<DeviceGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [creatingOpen, setCreatingOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [scope, setScope] = useState<AlertPolicy["scope"]>("fleet");
  const [target, setTarget] = useState("");
  const [priority, setPriority] = useState("P2");
  const [offline, setOffline] = useState("900");
  const [steps, setSteps] = useState<DraftStep[]>([emptyStep()]);

  const load = useCallback(async (background = false) => {
    if (!background) setLoading(true);
    try {
      const [p, i] = await Promise.all([api.listAlertPolicies(), api.listAlertIncidents("open")]);
      setPolicies(p.items);
      setIncidents(i.items);
      setError(null);
    } catch (e) {
      if (!background) setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to load alerting data");
    } finally {
      if (!background) setLoading(false);
    }
  }, []);
  useEffect(() => {
    load();
    api.listDeviceGroups().then((res) => setGroups(res.items)).catch(() => setGroups([]));
  }, [load]);
  const [live, setLive] = useLive(() => load(true), 10000);

  const changeState = async (incident: AlertIncident, state: string) => {
    try {
      await api.updateAlertIncident(incident.id, state);
      toast(`Incident ${state}`, "success");
      await load(true);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : `Could not mark the incident ${state}`, "error");
    }
  };
  const changeStateRef = useRef(changeState);
  changeStateRef.current = changeState;

  const onDelete = async (p: AlertPolicy) => {
    if (!(await confirmDialog(`Delete alert policy "${p.name}"? Devices it covers fall back to the next policy in the order device, group, tenant, fleet, or raise no alert if there is none.`))) return;
    try {
      await api.deleteAlertPolicy(p.id);
      toast("Alert policy deleted", "info");
      await load(true);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Could not delete the policy", "error");
    }
  };
  const onDeleteRef = useRef(onDelete);
  onDeleteRef.current = onDelete;

  const updateStep = (index: number, field: keyof DraftStep, value: string) =>
    setSteps((current) => current.map((step, i) => (i === index ? { ...step, [field]: value } : step)));

  const onCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setCreateError(null);
    setCreating(true);
    try {
      await api.createAlertPolicy({
        name,
        scope,
        ...(scope === "fleet" ? {} : scope === "device" ? { device_id: target } : scope === "group" ? { group_id: target } : { tenant_id: target }),
        fault_priorities: { offline: priority },
        // The API takes durations in nanoseconds.
        offline_after: Number(offline) * 1e9,
        steps: steps.map((step) => ({ after: Number(step.after) * 1e9, destination: step.destination, recipient: step.recipient })),
      });
      toast(`Alert policy "${name}" created`, "success");
      setName("");
      setTarget("");
      setSteps([emptyStep()]);
      setCreatingOpen(false);
      await load(true);
    } catch (err) {
      setCreateError(err instanceof ApiError ? err.message : "Failed to create alert policy");
    } finally {
      setCreating(false);
    }
  };

  const incidentColumns = useMemo<ColumnDef<AlertIncident, any>[]>(
    () => [
      {
        accessorKey: "priority",
        header: "Priority",
        cell: ({ getValue }) => <span className={`pill ${getValue() === "P1" ? "pill-danger" : "pill-warn"}`}>{getValue() as string}</span>,
      },
      { accessorKey: "summary", header: "What" },
      {
        accessorKey: "device_id",
        header: "Device",
        cell: ({ getValue }) => (
          <Link to={`/devices/${getValue() as string}`} title="Open this device">
            {(getValue() as string).slice(0, 8)}…
          </Link>
        ),
      },
      { accessorKey: "tenant_id", header: "Tenant", cell: ({ getValue }) => <span className="dim">{(getValue() as string) || "—"}</span> },
      { accessorKey: "first_seen_at", header: "Since", cell: ({ getValue }) => <span className="dim">{timeAgo(getValue() as string)}</span> },
      { accessorKey: "escalation_stage", header: "Escalation step", cell: ({ getValue }) => <span className="dim">{getValue() as number}</span> },
      {
        id: "actions",
        header: "",
        enableSorting: false,
        cell: ({ row }) => (
          <span className="row-actions">
            {(["acknowledged", "suppressed", "recovered"] as const).map((state) => (
              <button key={state} className="btn sm" disabled={!writable} onClick={() => changeStateRef.current(row.original, state)}>
                {state === "acknowledged" ? "Acknowledge" : state === "suppressed" ? "Suppress" : "Mark recovered"}
              </button>
            ))}
          </span>
        ),
      },
    ],
    [writable],
  );

  const groupName = useCallback((id?: string) => groups.find((g) => g.id === id)?.name ?? id ?? "—", [groups]);
  const policyColumns = useMemo<ColumnDef<AlertPolicy, any>[]>(
    () => [
      { accessorKey: "name", header: "Name" },
      {
        id: "scope",
        header: "Applies to",
        accessorFn: (p) => p.scope,
        cell: ({ row }) => {
          const p = row.original;
          const what = p.scope === "group" ? groupName(p.group_id) : p.scope === "device" ? `${(p.device_id ?? "").slice(0, 8)}…` : p.scope === "tenant" ? p.tenant_id : "";
          return (
            <span>
              {SCOPE_LABEL[p.scope]}
              {what ? <span className="dim"> · {what}</span> : null}
            </span>
          );
        },
      },
      { id: "priority", header: "Offline priority", accessorFn: (p) => p.fault_priorities?.offline ?? "—" },
      {
        id: "threshold",
        header: "Offline after",
        accessorFn: (p) => policyThresholdSeconds(p.offline_after),
        cell: ({ getValue }) => <span className="dim">{fmtSeconds(getValue() as number)}</span>,
      },
      { id: "steps", header: "Escalation steps", accessorFn: (p) => p.steps?.length ?? 0 },
      {
        id: "actions",
        header: "",
        enableSorting: false,
        cell: ({ row }) => (
          <button className="btn danger" disabled={!writable} onClick={() => onDeleteRef.current(row.original)}>
            Delete
          </button>
        ),
      },
    ],
    [writable, groupName],
  );

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" onClick={() => load()}>Retry</button>
        </div>
      )}

      <div className="page-bar">
        <p className="page-lede">
          An alert policy says when a device counts as offline and who is told, step by step. The most specific policy wins: device, then group, then
          tenant, then fleet.
        </p>
        <span className="page-actions">
          <button className={`btn live-toggle ${live ? "on" : ""}`} onClick={() => setLive((l) => !l)} title={live ? "Auto-refreshing every 10s — click to pause" : "Auto-refresh paused — click to resume"}>
            <span className="live-dot" /> {live ? "Live" : "Paused"}
          </button>
          <button className="btn primary" onClick={() => setCreatingOpen(true)} disabled={!writable}>
            New alert policy
          </button>
        </span>
      </div>

      {creatingOpen && (
        <Drawer title="New alert policy" onClose={() => setCreatingOpen(false)} wide>
          <form onSubmit={onCreate}>
            <div className="form-row">
              <label className="field">
                <span>Name</span>
                <input value={name} onChange={(e) => setName(e.target.value)} required />
              </label>
              <label className="field">
                <span>Applies to</span>
                <select
                  value={scope}
                  onChange={(e) => {
                    setScope(e.target.value as AlertPolicy["scope"]);
                    setTarget("");
                  }}
                >
                  {(Object.keys(SCOPE_LABEL) as AlertPolicy["scope"][]).map((s) => (
                    <option key={s} value={s}>{SCOPE_LABEL[s]}</option>
                  ))}
                </select>
              </label>
            </div>
            {scope === "group" && (
              <div className="form-row">
                <label className="field">
                  <span>Group</span>
                  <select value={target} onChange={(e) => setTarget(e.target.value)} required>
                    <option value="">Choose a group…</option>
                    {groups.map((g) => (
                      <option key={g.id} value={g.id}>{g.name} ({g.member_count})</option>
                    ))}
                  </select>
                </label>
              </div>
            )}
            {(scope === "device" || scope === "tenant") && (
              <div className="form-row">
                <label className="field">
                  <span>{scope === "device" ? "Device ID" : "Tenant ID"}</span>
                  <input value={target} onChange={(e) => setTarget(e.target.value)} required />
                </label>
              </div>
            )}
            <div className="form-row">
              <label className="field">
                <span>Priority when offline</span>
                <select value={priority} onChange={(e) => setPriority(e.target.value)}>
                  <option>P1</option>
                  <option>P2</option>
                  <option>P3</option>
                </select>
              </label>
              <label className="field">
                <span>Offline after (seconds without a check-in)</span>
                <input type="number" min="0" value={offline} onChange={(e) => setOffline(e.target.value)} required />
              </label>
            </div>

            <h4 className="form-section">Who is told, and when</h4>
            {steps.map((step, index) => (
              <div className="form-row" key={index}>
                <label className="field">
                  <span>Step {index + 1}: after (seconds)</span>
                  <input type="number" min="0" value={step.after} onChange={(e) => updateStep(index, "after", e.target.value)} required />
                </label>
                <label className="field">
                  <span>Send to</span>
                  <input value={step.destination} onChange={(e) => updateStep(index, "destination", e.target.value)} placeholder="nms, email, sms" required />
                </label>
                <label className="field">
                  <span>Recipient</span>
                  <input value={step.recipient} onChange={(e) => updateStep(index, "recipient", e.target.value)} placeholder="on-call identifier" required />
                </label>
                {steps.length > 1 && (
                  <button type="button" className="close-detail" aria-label={`Remove step ${index + 1}`} onClick={() => setSteps((current) => current.filter((_, i) => i !== index))}>
                    ✕
                  </button>
                )}
              </div>
            ))}
            {steps.length < 5 && (
              <div className="form-row">
                <button type="button" className="btn" onClick={() => setSteps((current) => [...current, emptyStep()])}>
                  + Add step
                </button>
              </div>
            )}

            {createError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{createError}</div>}
            <div className="form-row">
              <button className="btn primary" type="submit" disabled={creating || !name || (scope !== "fleet" && !target)}>
                {creating ? "Creating…" : "Create policy"}
              </button>
            </div>
          </form>
        </Drawer>
      )}

      <h3 className="section-title first">Open incidents</h3>
      {loading && incidents.length === 0 ? (
        <div className="loading">Loading incidents…</div>
      ) : (
        <DataTable data={incidents} columns={incidentColumns} getRowId={(i) => i.id} emptyMessage="No open incidents." />
      )}

      <h3 className="section-title">Alert policies</h3>
      <DataTable data={policies} columns={policyColumns} getRowId={(p) => p.id} emptyMessage="No alert policies yet. Without one, no device raises an alert." />
    </section>
  );
}
