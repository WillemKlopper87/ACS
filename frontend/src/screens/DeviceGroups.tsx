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
import { DevicePicker } from "../components/DevicePicker";
import { StatusBadge } from "../components/StatusBadge";
import type { Device } from "../api/types";

export function DeviceGroups() {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [groups, setGroups] = useState<DeviceGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<DeviceGroup | null>(null);
  const [memberInput, setMemberInput] = useState("");
  const [memberError, setMemberError] = useState<string | null>(null);
  const [picking, setPicking] = useState(false);
  // Membership is stored as device ids. Rendering those ids raw made the
  // list unreadable, so the fleet is fetched once and used to resolve each
  // id to something an operator recognises.
  const [fleet, setFleet] = useState<Device[]>([]);

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

  useEffect(() => {
    api.listDevices(1, 500).then((res) => setFleet(res.items)).catch(() => setFleet([]));
  }, []);

  const deviceById = useMemo(() => new Map(fleet.map((d) => [d.id, d])), [fleet]);

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

  async function onDelete(g: DeviceGroup) {
    const members = g.member_count === 1 ? "1 member" : `${g.member_count} members`;
    if (!window.confirm(`Delete group "${g.name}" (${members})? Templates, schedules and rollouts that target it will lose their target. This cannot be undone.`)) return;
    try {
      await api.deleteDeviceGroup(g.id);
      if (selected?.id === g.id) setSelected(null);
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

  async function addMembers(ids: string[]) {
    if (!selected || ids.length === 0) return;
    await api.addDeviceGroupMembers(selected.id, ids);
    toast(`${ids.length} device${ids.length === 1 ? "" : "s"} added to "${selected.name}"`, "success");
    await openGroup(selected);
    await load();
  }

  // The paste box takes serial numbers, not device ids: a serial is printed
  // on the device and is what appears in a spreadsheet or a ticket, whereas
  // the id is an internal UUID. Anything that does not resolve is reported
  // rather than silently dropped.
  async function onAddPastedSerials() {
    if (!selected) return;
    setMemberError(null);
    const tokens = memberInput
      .split(/[\s,;]+/)
      .map((t) => t.trim())
      .filter(Boolean);
    if (tokens.length === 0) return;

    const bySerial = new Map(fleet.map((d) => [d.serial_number?.toLowerCase() ?? "", d]));
    const byOUISerial = new Map(fleet.map((d) => [d.oui_serial?.toLowerCase() ?? "", d]));
    const byLabel = new Map(fleet.filter((d) => d.label).map((d) => [d.label!.toLowerCase(), d]));
    const ids: string[] = [];
    const unknown: string[] = [];
    const wrongCustomer: string[] = [];
    for (const token of tokens) {
      const key = token.toLowerCase();
      const match = bySerial.get(key) ?? byOUISerial.get(key) ?? byLabel.get(key) ?? deviceById.get(token);
      if (!match) {
        unknown.push(token);
        continue;
      }
      // The API enforces this too, but its rejection names the device by
      // UUID — which is the very thing an operator cannot recognise. Check
      // it here so the message can say "SN000002" instead.
      if (selected.customer_id && match.customer_id !== selected.customer_id) {
        wrongCustomer.push(match.serial_number || token);
        continue;
      }
      ids.push(match.id);
    }

    const problems: string[] = [];
    if (unknown.length > 0) problems.push(`not found: ${unknown.join(", ")}`);
    if (wrongCustomer.length > 0) {
      problems.push(`belongs to another customer: ${wrongCustomer.join(", ")}`);
    }

    if (ids.length === 0) {
      setMemberError(problems.join("; ") || "Nothing to add");
      return;
    }
    try {
      await addMembers(ids);
      setMemberInput("");
      setMemberError(problems.length > 0 ? `Added ${ids.length}. Skipped — ${problems.join("; ")}` : null);
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
            className="btn danger"
            disabled={!writable}
            onClick={(e) => {
              e.stopPropagation();
              onDeleteRef.current(row.original);
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
              <label className="field">
                <span>Name</span>
                <input value={name} onChange={(e) => setName(e.target.value)} required />
              </label>
            </div>
            <div className="form-row">
              <label className="field">
                <span>Description (optional)</span>
                <input value={description} onChange={(e) => setDescription(e.target.value)} />
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
                <button className="btn primary" onClick={() => setPicking(true)} disabled={!writable}>
                  Add devices…
                </button>
                <span className="dim" style={{ fontSize: "0.8rem" }}>
                  or paste serial numbers:
                </span>
              </div>
              <div className="form-row">
                <input aria-label="Serial numbers (comma or space separated)"
                  placeholder="SN000123, SN000124…"
                  value={memberInput}
                  onChange={(e) => setMemberInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") onAddPastedSerials();
                  }}
                />
                <button className="btn" onClick={onAddPastedSerials} disabled={!writable || !memberInput.trim()}>
                  Add
                </button>
              </div>
              {memberError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{memberError}</div>}
              <ul className="member-list">
                {(selected.device_ids ?? []).length === 0 && <li className="dim">No members yet.</li>}
                {(selected.device_ids ?? []).map((id) => {
                  const d = deviceById.get(id);
                  return (
                    <li key={id}>
                      <span className="pick-serial">{d?.label || d?.serial_number || id}</span>
                      <span className="dim">
                        {d
                          ? `${d.label ? `${d.serial_number} · ` : ""}${d.manufacturer} ${d.product_class}`
                          : "not in the first 500 devices"}
                        {d?.location ? ` · ${d.location}` : ""}
                      </span>
                      {d && <StatusBadge value={d.online_status} />}
                      <button
                        className="close-detail"
                        disabled={!writable}
                        aria-label={`Remove ${d?.serial_number ?? id} from ${selected.name}`}
                        onClick={() => onRemoveMember(id)}
                      >
                        ✕
                      </button>
                    </li>
                  );
                })}
              </ul>
            </>
          ) : (
            <p className="dim" style={{ margin: 0 }}>
              Click a group in the table below to manage its members.
            </p>
          )}
        </div>
      </div>

      {picking && selected && (
        <DevicePicker
          title={`Add devices to "${selected.name}"`}
          confirmLabel="Add to group"
          excludeIds={selected.device_ids ?? []}
          customerId={selected.customer_id}
          customerLabel={customerName(customers, selected.customer_id)}
          onConfirm={addMembers}
          onClose={() => setPicking(false)}
        />
      )}

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
