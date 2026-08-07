import type { ColumnDef } from "@tanstack/react-table";
import { useEffect, useMemo, useState } from "react";
import { api, ApiError } from "../api/client";
import type { ConfigTemplate, TemplateParameter } from "../api/types";
import { DataTable } from "../components/DataTable";
import { fmtTime, timeAgo } from "../lib/format";
import { useAuth } from "../auth/AuthContext";
import { canWrite } from "../auth/roles";
import { toast } from "../lib/toast";
import { useEscape } from "../lib/hotkeys";

const emptyRow: TemplateParameter = { name: "", value: "", type: "string" };

// Config Templates: a named, reusable, multi-parameter set (e.g. a full
// WiFi profile — SSID + passphrase + channel together) that can be
// bulk-applied on demand to a pasted device-ID list or a device group,
// and optionally auto-applied the moment a matching device's first
// BOOTSTRAP Inform arrives. Built directly from a concrete ask: Fleet
// Control's bulk SET_PARAMETER only ever supported one hardcoded
// parameter (WiFi SSID) — this is the reusable, multi-parameter version
// of that, not a replacement for it.
export function ConfigTemplates() {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [items, setItems] = useState<ConfigTemplate[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<ConfigTemplate | null>(null);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [modelFilter, setModelFilter] = useState("");
  const [autoApply, setAutoApply] = useState(false);
  const [rows, setRows] = useState<TemplateParameter[]>([{ ...emptyRow }]);
  const [createError, setCreateError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const [applyTarget, setApplyTarget] = useState<"devices" | "group">("devices");
  const [deviceIdsInput, setDeviceIdsInput] = useState("");
  const [groupIdInput, setGroupIdInput] = useState("");
  const [applying, setApplying] = useState(false);

  async function load() {
    setLoading(true);
    setError(null);
    try {
      const res = await api.listTemplates();
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

  useEscape(() => setSelected(null), selected !== null);

  function updateRow(i: number, patch: Partial<TemplateParameter>) {
    setRows((prev) => prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  }
  function addRow() {
    setRows((prev) => [...prev, { ...emptyRow }]);
  }
  function removeRow(i: number) {
    setRows((prev) => (prev.length === 1 ? prev : prev.filter((_, idx) => idx !== i)));
  }

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    setCreateError(null);
    const validRows = rows.filter((r) => r.name.trim() && r.value.trim());
    if (validRows.length === 0) {
      setCreateError("At least one parameter (name + value) is required");
      return;
    }
    if (autoApply && !modelFilter.trim()) {
      setCreateError("Auto-apply requires a model filter — applying to every device on first contact isn't supported");
      return;
    }
    setCreating(true);
    try {
      await api.createTemplate({
        name,
        description: description || undefined,
        parameters: validRows,
        model_filter: modelFilter || undefined,
        auto_apply: autoApply,
      });
      toast(`Template "${name}" created`, "success");
      setName("");
      setDescription("");
      setModelFilter("");
      setAutoApply(false);
      setRows([{ ...emptyRow }]);
      await load();
    } catch (e) {
      setCreateError(e instanceof ApiError ? e.message : "Failed to create template");
    } finally {
      setCreating(false);
    }
  }

  async function onDelete(id: string) {
    try {
      await api.deleteTemplate(id);
      if (selected?.id === id) setSelected(null);
      toast("Template deleted", "info");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to delete template", "error");
    }
  }

  async function onApply() {
    if (!selected) return;
    setApplying(true);
    try {
      const target =
        applyTarget === "devices"
          ? { device_ids: deviceIdsInput.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean) }
          : { group_id: groupIdInput.trim() };
      if (applyTarget === "devices" && (target.device_ids?.length ?? 0) === 0) {
        toast("Enter at least one device ID", "error");
        return;
      }
      if (applyTarget === "group" && !groupIdInput.trim()) {
        toast("Enter a group ID", "error");
        return;
      }
      const res = await api.applyTemplate(selected.id, target);
      toast(`Applied "${selected.name}": ${res.succeeded} of ${res.requested} devices queued successfully`, res.succeeded === res.requested ? "success" : "info");
      if (res.results.some((r) => r.error)) {
        const failed = res.results.filter((r) => r.error).slice(0, 5);
        toast(`Failures: ${failed.map((f) => `${f.device_id.slice(0, 8)}…: ${f.error}`).join("; ")}`, "error", 8000);
      }
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to apply template", "error");
    } finally {
      setApplying(false);
    }
  }

  const columns = useMemo<ColumnDef<ConfigTemplate, any>[]>(
    () => [
      { accessorKey: "name", header: "Name" },
      { accessorKey: "description", header: "Description", cell: ({ getValue }) => <span className="dim">{(getValue() as string) || "—"}</span> },
      {
        id: "parameters",
        header: "Parameters",
        accessorFn: (t) => t.parameters.length,
        cell: ({ row }) => <span className="dim">{row.original.parameters.length} parameter{row.original.parameters.length === 1 ? "" : "s"}</span>,
      },
      {
        accessorKey: "auto_apply",
        header: "Auto-apply",
        cell: ({ row }) =>
          row.original.auto_apply ? (
            <span className="pill pill-ok">on BOOTSTRAP: {row.original.model_filter}</span>
          ) : (
            <span className="dim">off</span>
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
          <button
            className="btn"
            disabled={!writable}
            onClick={(e) => {
              e.stopPropagation();
              onDelete(row.original.id);
            }}
          >
            Delete
          </button>
        ),
      },
    ],
    [writable],
  );

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" style={{ marginLeft: "0.6rem" }} onClick={load}>Retry</button>
        </div>
      )}

      <div className="split two-col">
        <div className="panel">
          <h3>Create config template</h3>
          <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
            A named, reusable set of parameters — e.g. a WiFi profile (SSID + passphrase + channel together). Paths are plain TR-181 (Device:2), the same convention every write in this app already uses — works across manufacturers/models sharing that data model root.
          </p>
          <form onSubmit={onCreate}>
            <div className="form-row">
              <input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} required disabled={!writable} />
              <input placeholder="Description (optional)" value={description} onChange={(e) => setDescription(e.target.value)} disabled={!writable} />
            </div>

            {rows.map((row, i) => (
              <div className="form-row" key={i}>
                <input
                  placeholder="Parameter path, e.g. Device.WiFi.SSID.1.SSID"
                  value={row.name}
                  onChange={(e) => updateRow(i, { name: e.target.value })}
                  disabled={!writable}
                />
                <input
                  placeholder="Value"
                  value={row.value}
                  onChange={(e) => updateRow(i, { value: e.target.value })}
                  disabled={!writable}
                />
                <select className="chip" value={row.type} onChange={(e) => updateRow(i, { type: e.target.value })} disabled={!writable}>
                  <option value="string">string</option>
                  <option value="int">int</option>
                  <option value="boolean">boolean</option>
                </select>
                <button type="button" className="close-detail" onClick={() => removeRow(i)} disabled={!writable || rows.length === 1} title="Remove parameter">
                  ✕
                </button>
              </div>
            ))}
            <div className="form-row">
              <button type="button" className="btn" onClick={addRow} disabled={!writable}>
                + Add parameter
              </button>
            </div>

            <div className="form-row">
              <label className="dim" style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.8rem", flex: 1 }}>
                <input type="checkbox" checked={autoApply} onChange={(e) => setAutoApply(e.target.checked)} disabled={!writable} />
                Auto-apply on first BOOTSTRAP to devices matching:
              </label>
              <input
                placeholder="Model filter (manufacturer or product class)"
                value={modelFilter}
                onChange={(e) => setModelFilter(e.target.value)}
                disabled={!writable}
              />
            </div>

            {createError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{createError}</div>}
            <div className="form-row">
              <button type="submit" className="btn primary" disabled={creating || !writable || !name}>
                {creating ? "Creating…" : "Create template"}
              </button>
            </div>
          </form>
        </div>

        <div className="panel">
          <h3>
            {selected ? selected.name : "Select a template"}
            {selected && (
              <button className="close-detail" onClick={() => setSelected(null)}>
                ✕
              </button>
            )}
          </h3>
          {selected ? (
            <>
              <ul style={{ listStyle: "none", margin: "0 0 0.9rem", padding: 0, maxHeight: "10rem", overflow: "auto" }}>
                {selected.parameters.map((p) => (
                  <li key={p.name} className="param-row" style={{ padding: "0.3rem 0" }}>
                    <span className="path">{p.name}</span>
                    <span className="val">{p.value}</span>
                  </li>
                ))}
              </ul>
              <div className="form-row" style={{ marginTop: 0 }}>
                <select className="chip" value={applyTarget} onChange={(e) => setApplyTarget(e.target.value as "devices" | "group")}>
                  <option value="devices">Device IDs</option>
                  <option value="group">Device group</option>
                </select>
                {applyTarget === "devices" ? (
                  <input
                    placeholder="Device IDs (comma or space separated)"
                    value={deviceIdsInput}
                    onChange={(e) => setDeviceIdsInput(e.target.value)}
                    disabled={!writable}
                  />
                ) : (
                  <input
                    placeholder="Group ID"
                    value={groupIdInput}
                    onChange={(e) => setGroupIdInput(e.target.value)}
                    disabled={!writable}
                  />
                )}
              </div>
              <div className="form-row">
                <button
                  className="btn primary"
                  onClick={onApply}
                  disabled={applying || !writable}
                  title={writable ? undefined : "Read-only role — applying a template requires operator or admin"}
                >
                  {applying ? "Applying…" : "Apply template"}
                </button>
              </div>
              <p className="dim" style={{ fontSize: "0.76rem", marginTop: "0.6rem", marginBottom: 0 }}>
                Same selection convention Fleet Control uses: paste device IDs directly, or target a device group instead. Each device gets its own independent job — one failure doesn't block the rest.
              </p>
            </>
          ) : (
            <p className="dim" style={{ margin: 0 }}>
              Click a template in the table below to apply it.
            </p>
          )}
        </div>
      </div>

      {loading && items.length === 0 ? (
        <div className="loading">Loading templates…</div>
      ) : (
        <div style={{ marginTop: "1.1rem" }}>
          <DataTable data={items} columns={columns} getRowId={(t) => t.id} onRowClick={setSelected} emptyMessage="No config templates yet." />
        </div>
      )}
    </section>
  );
}
