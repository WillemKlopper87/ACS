import type { ColumnDef } from "@tanstack/react-table";
import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { DeviceGroup } from "../api/types";
import { DataTable } from "../components/DataTable";
import { fmtTime, timeAgo } from "../lib/format";
import { useAuth } from "../auth/useAuth";
import { canWrite } from "../auth/roles";
import { toast } from "../lib/toast";
import { useEscape } from "../lib/hotkeys";
import { useCustomers, customerName } from "../lib/useCustomers";

export function DeviceGroups() {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [groups, setGroups] = useState<DeviceGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<DeviceGroup | null>(null);
  const [memberInput, setMemberInput] = useState("");
  const [memberError, setMemberError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [customerId, setCustomerId] = useState("");
  const [createError, setCreateError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const customers = useCustomers();

  async function load() {
    setLoading(true);
    setError(null);
    try {
      const res = await api.listDeviceGroups();
      setGroups(res.items);
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

  async function openGroup(g: DeviceGroup) {
    try {
      const full = await api.getDeviceGroup(g.id);
      setSelected(full);
    } catch {
      setSelected(g);
    }
  }

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    setCreateError(null);
    setCreating(true);
    try {
      await api.createDeviceGroup(name, description || undefined, customerId || null);
      toast(`Group "${name}" created`, "success");
      setName("");
      setDescription("");
      setCustomerId("");
      await load();
    } catch (e) {
      setCreateError(e instanceof ApiError ? e.message : "Failed to create group");
    } finally {
      setCreating(false);
    }
  }

  async function onDelete(id: string) {
    try {
      await api.deleteDeviceGroup(id);
      if (selected?.id === id) setSelected(null);
      toast("Group deleted", "info");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to delete group", "error");
    }
  }

  // The column definitions are memoized on `writable` only; the delete
  // handler is read through a ref so a fresh closure per render doesn't
  // rebuild the table columns (lint: react-hooks/exhaustive-deps).
  const onDeleteRef = useRef(onDelete);
  onDeleteRef.current = onDelete;

  async function onAddMembers() {
    if (!selected) return;
    setMemberError(null);
    const ids = memberInput
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    if (ids.length === 0) return;
    try {
      await api.addDeviceGroupMembers(selected.id, ids);
      toast(`${ids.length} member${ids.length === 1 ? "" : "s"} added`, "success");
      setMemberInput("");
      await openGroup(selected);
      await load();
    } catch (e) {
      setMemberError(e instanceof ApiError ? e.message : "Failed to add members");
    }
  }

  async function onRemoveMember(deviceId: string) {
    if (!selected) return;
    try {
      await api.removeDeviceGroupMember(selected.id, deviceId);
      await openGroup(selected);
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to remove member", "error");
    }
  }

  const columns = useMemo<ColumnDef<DeviceGroup, any>[]>(
    () => [
      { accessorKey: "name", header: "Name" },
      { accessorKey: "description", header: "Description", cell: ({ getValue }) => <span className="dim">{(getValue() as string) || "—"}</span> },
      { accessorKey: "customer_id", header: "Customer", cell: ({ getValue }) => <span className="dim">{customerName(customers, getValue() as string | null)}</span> },
      { accessorKey: "member_count", header: "Members" },
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
              onDeleteRef.current(row.original.id);
            }}
          >
            Delete
          </button>
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

      <div className="split two-col">
        <div className="panel">
          <h3>Create group</h3>
          <form onSubmit={onCreate}>
            <div className="form-row">
              <input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} required />
            </div>
            <div className="form-row">
              <input placeholder="Description (optional)" value={description} onChange={(e) => setDescription(e.target.value)} />
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
              <button type="submit" className="btn primary" disabled={creating || !writable || !name}>
                {creating ? "Creating…" : "Create group"}
              </button>
            </div>
          </form>
        </div>

        <div className="panel">
          <h3>
            {selected ? `Members — ${selected.name}` : "Select a group"}
            {selected && (
              <button className="close-detail" onClick={() => setSelected(null)}>
                ✕
              </button>
            )}
          </h3>
          {selected ? (
            <>
              <div className="form-row">
                <input
                  placeholder="Device IDs (comma or space separated)"
                  value={memberInput}
                  onChange={(e) => setMemberInput(e.target.value)}
                />
                <button className="btn" onClick={onAddMembers} disabled={!writable || !memberInput.trim()}>
                  Add
                </button>
              </div>
              {memberError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{memberError}</div>}
              <ul style={{ listStyle: "none", margin: "0.75rem 0 0", padding: 0, maxHeight: "16rem", overflow: "auto" }}>
                {(selected.device_ids ?? []).length === 0 && <li className="dim">No members yet.</li>}
                {(selected.device_ids ?? []).map((id) => (
                  <li
                    key={id}
                    style={{
                      display: "flex",
                      justifyContent: "space-between",
                      alignItems: "center",
                      fontFamily: "var(--font-mono)",
                      fontSize: "0.8rem",
                      padding: "0.3rem 0",
                      borderBottom: "1px solid var(--border)",
                    }}
                  >
                    <span>{id}</span>
                    <button className="close-detail" disabled={!writable} onClick={() => onRemoveMember(id)}>
                      ✕
                    </button>
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <p className="dim" style={{ margin: 0 }}>
              Click a group in the table below to manage its members.
            </p>
          )}
        </div>
      </div>

      {loading && groups.length === 0 ? (
        <div className="loading">Loading groups…</div>
      ) : (
        <div style={{ marginTop: "1.1rem" }}>
          <DataTable data={groups} columns={columns} getRowId={(g) => g.id} onRowClick={openGroup} emptyMessage="No device groups yet." />
        </div>
      )}
    </section>
  );
}
