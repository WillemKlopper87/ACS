import type { ColumnDef } from "@tanstack/react-table";
import { useEffect, useMemo, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Policy } from "../api/types";
import { DataTable } from "../components/DataTable";
import { fmtTime, timeAgo } from "../lib/format";
import { useAuth } from "../auth/useAuth";
import { canWrite } from "../auth/roles";
import { toast } from "../lib/toast";
import { useCustomers, customerName } from "../lib/useCustomers";

export function Policies() {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [items, setItems] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [modelFilter, setModelFilter] = useState("");
  const [parameterName, setParameterName] = useState("");
  const [desiredValue, setDesiredValue] = useState("");
  const [customerId, setCustomerId] = useState("");
  const [createError, setCreateError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const customers = useCustomers();

  async function load() {
    setLoading(true);
    setError(null);
    try {
      const res = await api.listPolicies();
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
      await api.createPolicy({
        name,
        model_filter: modelFilter || undefined,
        parameter_name: parameterName,
        desired_value: desiredValue,
        customer_id: customerId || null,
      });
      toast(`Policy "${name}" created`, "success");
      setName("");
      setModelFilter("");
      setParameterName("");
      setDesiredValue("");
      setCustomerId("");
      await load();
    } catch (e) {
      setCreateError(e instanceof ApiError ? e.message : "Failed to create policy");
    } finally {
      setCreating(false);
    }
  }

  async function onToggle(p: Policy) {
    try {
      await api.setPolicyEnabled(p.id, !p.enabled);
      toast(`"${p.name}" ${p.enabled ? "disabled" : "enabled"}`, "info");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to update policy", "error");
    }
  }

  async function onDelete(p: Policy) {
    if (!window.confirm(`Delete policy "${p.name}"? Devices will stop being corrected back to "${p.desired_value}". This cannot be undone.`)) return;
    try {
      await api.deletePolicy(p.id);
      toast("Policy deleted", "info");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to delete policy", "error");
    }
  }

  const columns = useMemo<ColumnDef<Policy, any>[]>(
    () => [
      { accessorKey: "name", header: "Name" },
      { accessorKey: "customer_id", header: "Customer", cell: ({ getValue }) => <span className="dim">{customerName(customers, getValue() as string | null)}</span> },
      { accessorKey: "model_filter", header: "Model Filter", cell: ({ getValue }) => <span className="dim">{(getValue() as string) || "fleet-wide"}</span> },
      { accessorKey: "parameter_name", header: "Parameter" },
      { accessorKey: "desired_value", header: "Desired Value", cell: ({ getValue }) => <span style={{ color: "var(--accent)" }}>{getValue() as string}</span> },
      {
        accessorKey: "enabled",
        header: "Status",
        cell: ({ row }) => (
          <span className={`pill ${row.original.enabled ? "pill-ok" : "pill-neutral"}`}>{row.original.enabled ? "enabled" : "disabled"}</span>
        ),
      },
      {
        accessorKey: "created_at",
        header: "Created",
        cell: ({ getValue }) => {
          const v = getValue() as string;
          return <span className="dim" title={`${fmtTime(v)} UTC`}>{timeAgo(v)}</span>;
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
            <button className="btn danger" disabled={!writable} onClick={() => onDelete(row.original)}>
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
        <h3>Create compliance policy</h3>
        <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
          Devices matching the model filter get this parameter auto-corrected the moment a check-in reports a drifted value.
        </p>
        <form onSubmit={onCreate}>
          <div className="form-row">
            <label className="field">
              <span>Name</span>
              <input value={name} onChange={(e) => setName(e.target.value)} required />
            </label>
            <label className="field">
              <span>Model filter (optional)</span>
              <input placeholder="manufacturer or product class" value={modelFilter} onChange={(e) => setModelFilter(e.target.value)} />
            </label>
          </div>
          <div className="form-row">
            <label className="field">
              <span>Parameter name</span>
              <input placeholder="e.g. Device.WiFi.SSID.1.SSID" value={parameterName} onChange={(e) => setParameterName(e.target.value)} required />
            </label>
            <label className="field">
              <span>Desired value</span>
              <input value={desiredValue} onChange={(e) => setDesiredValue(e.target.value)} required />
            </label>
          </div>
          <div className="form-row">
            <label className="field">
              <span>Customer</span>
              <select value={customerId} onChange={(e) => setCustomerId(e.target.value)}>
                <option value="">Platform-wide (no customer)</option>
                {customers.map((c) => (
                  <option key={c.id} value={c.id}>{c.name}</option>
                ))}
              </select>
            </label>
          </div>
          {createError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{createError}</div>}
          <div className="form-row">
            <button type="submit" className="btn primary" disabled={creating || !writable || !name || !parameterName || !desiredValue}>
              {creating ? "Creating…" : "Create policy"}
            </button>
          </div>
        </form>
      </div>

      {loading && items.length === 0 ? (
        <div className="loading">Loading policies…</div>
      ) : (
        <div style={{ marginTop: "1.1rem" }}>
          <DataTable data={items} columns={columns} getRowId={(p) => p.id} emptyMessage="No policies yet." />
        </div>
      )}
    </section>
  );
}
