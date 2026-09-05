import type { ColumnDef } from "@tanstack/react-table";
import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Customer, Operator, OperatorScope, Permission, Region, Role, RolePermissionsMatrix } from "../api/types";
import { DataTable } from "../components/DataTable";
import { fmtTime, timeAgo } from "../lib/format";
import { useAuth } from "../auth/useAuth";
import { canAdmin } from "../auth/roles";
import { toast } from "../lib/toast";

const ROLES: Role[] = ["readonly", "noc", "manager", "superadmin"];
const ROLE_TONE: Record<Role, string> = { superadmin: "pill-warn", manager: "pill-ok", noc: "pill-ok", readonly: "pill-neutral" };

const PERMISSION_LABEL: Record<Permission, string> = {
  "devices.write": "Write device parameters / reboot / factory-reset",
  connection_request: "Trigger Connection Request",
  "diagnostics.run": "Run diagnostics (ping/traceroute/discover)",
  "firmware.manage": "Manage firmware images & rollouts",
  "template.manage": "Manage config templates",
  "policy.manage": "Manage compliance policies",
  "schedule.manage": "Manage scheduled jobs",
  "group.manage": "Manage device groups",
  "credential.manage": "Rotate/activate/revoke device credentials",
  "cli.access": "SSH/Telnet console & web GUI access",
  bulk_actions: "Bulk device actions",
  "upload.request": "Request config backup / log upload",
};

// Closes the build plan's Phase 6 gap (creating operators had no UI) and
// the admin-platform backlog's RBAC expansion: 4 role tiers instead of 3,
// plus the superadmin-configurable permission matrix for the curated
// capability list (internal/operators.AllPermissions). Superadmin-only
// screen, both here (canAdmin gates it) and on the server (requireRole,
// the real enforcement boundary — same as every other write in this app).
export function Operators() {
  const { role, username: myUsername } = useAuth();
  const admin = canAdmin(role);
  const [items, setItems] = useState<Operator[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [newRole, setNewRole] = useState<Role>("noc");
  const [createError, setCreateError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const [matrix, setMatrix] = useState<RolePermissionsMatrix | null>(null);
  const [resetTarget, setResetTarget] = useState<Operator | null>(null);
  const [resetPassword, setResetPassword] = useState("");

  const [regions, setRegions] = useState<Region[]>([]);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [scopeTarget, setScopeTarget] = useState<Operator | null>(null);
  const [scopeSelection, setScopeSelection] = useState<Set<string>>(new Set()); // "region:id" / "customer:id"
  const [globalAccess, setGlobalAccess] = useState(false);

  async function load() {
    setLoading(true);
    setError(null);
    try {
      const [ops, perms, regs, custs] = await Promise.all([api.listOperators(), api.getRolePermissions(), api.listRegions(), api.listCustomers()]);
      setItems(ops.items);
      setMatrix(perms);
      setRegions(regs.items);
      setCustomers(custs.items);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API — is cmd/api running?");
    } finally {
      setLoading(false);
    }
  }

  async function openScopes(op: Operator) {
    setScopeTarget(op);
    setGlobalAccess(op.global_access ?? false);
    try {
      const res = await api.getOperatorScopes(op.id);
      setScopeSelection(new Set(res.items.map((s) => `${s.type}:${s.id}`)));
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to load scopes", "error");
    }
  }

  async function saveGlobalAccess() {
    if (!scopeTarget) return;
    try {
      await api.setOperatorGlobalAccess(scopeTarget.id, globalAccess);
      toast(`Global access ${globalAccess ? "granted to" : "revoked from"} "${scopeTarget.username}"`, "success");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to update global access", "error");
    }
  }

  function toggleScope(key: string) {
    setScopeSelection((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  async function saveScopes() {
    if (!scopeTarget) return;
    const scopes: OperatorScope[] = Array.from(scopeSelection).map((key) => {
      const [type, id] = key.split(":");
      return { type: type as "region" | "customer", id };
    });
    try {
      await api.setOperatorScopes(scopeTarget.id, scopes);
      toast(`Scopes saved for "${scopeTarget.username}"`, "success");
      setScopeTarget(null);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to save scopes", "error");
    }
  }

  useEffect(() => {
    load();
  }, []);

  // Offboarding: the account stops working immediately (the server also
  // revokes the sessions it already holds) but the row stays, so every
  // audit_log entry attributing an action to them still resolves.
  async function toggleDisabled(op: Operator) {
    const disabling = !op.disabled_at;
    if (
      disabling &&
      !window.confirm(
        `Disable "${op.username}"? They are signed out immediately and cannot sign in again until re-enabled. ` +
          `Their audit history is kept.`,
      )
    ) {
      return;
    }
    try {
      await api.setOperatorDisabled(op.id, disabling);
      toast(`${disabling ? "Disabled" : "Re-enabled"} "${op.username}"`, "success");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to update the operator", "error");
    }
  }

  // Read through a ref for the same reason the other list screens do: a
  // fresh closure per render would rebuild the memoized column defs.
  const toggleDisabledRef = useRef(toggleDisabled);
  toggleDisabledRef.current = toggleDisabled;

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    setCreateError(null);
    setCreating(true);
    try {
      await api.createOperator(username, password, newRole, email || undefined);
      toast(`Operator "${username}" created`, "success");
      setUsername("");
      setEmail("");
      setPassword("");
      setNewRole("noc");
      await load();
    } catch (e) {
      setCreateError(e instanceof ApiError ? e.message : "Failed to create operator");
    } finally {
      setCreating(false);
    }
  }

  async function togglePermission(r: Role, perm: Permission, current: boolean) {
    if (!matrix) return;
    // Optimistic update — the matrix is a grid of many small toggles, and
    // waiting on a round-trip per click would make it feel unresponsive.
    setMatrix({ ...matrix, matrix: { ...matrix.matrix, [r]: { ...matrix.matrix[r], [perm]: !current } } });
    try {
      await api.setRolePermission(r, perm, !current);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to update permission", "error");
      await load(); // roll back to server truth on failure
    }
  }

  async function onResetPassword(e: React.FormEvent) {
    e.preventDefault();
    if (!resetTarget) return;
    try {
      await api.resetOperatorPassword(resetTarget.id, resetPassword);
      toast(`Password reset for "${resetTarget.username}"`, "success");
      setResetTarget(null);
      setResetPassword("");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to reset password", "error");
    }
  }

  const columns = useMemo<ColumnDef<Operator, any>[]>(
    () => [
      {
        accessorKey: "username",
        header: "Username",
        cell: ({ getValue }) => (
          <span>
            {getValue() as string}
            {getValue() === myUsername && <span className="dim"> (you)</span>}
          </span>
        ),
      },
      { accessorKey: "email", header: "Email", cell: ({ getValue }) => <span className="dim">{(getValue() as string) || "—"}</span> },
      {
        accessorKey: "role",
        header: "Role",
        cell: ({ getValue }) => {
          const r = getValue() as Role;
          return <span className={`pill ${ROLE_TONE[r]}`}>{r}</span>;
        },
      },
      {
        id: "global_access",
        header: "Global Access",
        cell: ({ row }) =>
          row.original.role === "superadmin" ? (
            <span className="dim">—</span>
          ) : (
            <span className={`pill ${row.original.global_access ? "pill-warn" : "pill-neutral"}`}>
              {row.original.global_access ? "granted" : "scoped"}
            </span>
          ),
      },
      {
        id: "status",
        header: "Status",
        cell: ({ row }) =>
          row.original.disabled_at ? (
            <span className="pill pill-danger" title={`Disabled ${fmtTime(row.original.disabled_at)} UTC`}>
              disabled
            </span>
          ) : (
            <span className="pill pill-ok">active</span>
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
          <span style={{ display: "flex", gap: "0.4rem" }}>
            <button className="btn sm" onClick={() => setResetTarget(row.original)}>
              Reset password
            </button>
            {row.original.role !== "superadmin" && (
              <button className="btn sm" onClick={() => openScopes(row.original)}>
                Scopes
              </button>
            )}
            {/* Offboarding. Self-disable is refused server-side too; the
                button is hidden rather than left to fail, since there is
                no case where an operator wants to lock themselves out. */}
            {row.original.username !== myUsername && (
              <button
                className={row.original.disabled_at ? "btn sm" : "btn danger sm"}
                onClick={() => toggleDisabledRef.current(row.original)}
              >
                {row.original.disabled_at ? "Re-enable" : "Disable"}
              </button>
            )}
          </span>
        ),
      },
    ],
    [myUsername],
  );

  if (!admin) {
    return (
      <section>
        <div className="banner error">Operator management requires the superadmin role.</div>
      </section>
    );
  }

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" onClick={load}>Retry</button>
        </div>
      )}

      <div className="panel">
        <h3>Create operator</h3>
        <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
          readonly can only view; noc/manager's exact capabilities are set in the permission matrix below; superadmin
          can also manage operators and permissions. Email is optional — only needed for self-service password reset.
        </p>
        <form onSubmit={onCreate}>
          <div className="form-row">
            <label className="field">
              <span>Username</span>
              <input value={username} onChange={(e) => setUsername(e.target.value)} required autoComplete="off" />
            </label>
            <label className="field">
              <span>Email (optional)</span>
              <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="off" />
            </label>
            <label className="field">
              <span>Password</span>
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                autoComplete="new-password"
              />
            </label>
            <label className="field">
              <span>Role</span>
              <select value={newRole} onChange={(e) => setNewRole(e.target.value as Role)}>
                {ROLES.map((r) => (
                  <option key={r} value={r}>{r}</option>
                ))}
              </select>
            </label>
          </div>
          {createError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{createError}</div>}
          <div className="form-row">
            <button type="submit" className="btn primary" disabled={creating || !username.trim() || !password}>
              {creating ? "Creating…" : "Create operator"}
            </button>
          </div>
        </form>
      </div>

      {resetTarget && (
        <div className="panel" style={{ marginTop: "1.1rem" }}>
          <h3>
            Reset password for {resetTarget.username}
            <button className="close-detail" onClick={() => setResetTarget(null)}>✕</button>
          </h3>
          <form onSubmit={onResetPassword}>
            <div className="form-row" style={{ marginTop: 0 }}>
              <input aria-label="New password (min 8 characters)"
                type="password"
                placeholder="New password (min 8 characters)"
                value={resetPassword}
                onChange={(e) => setResetPassword(e.target.value)}
                required
                minLength={8}
                autoComplete="new-password"
              />
              <button type="submit" className="btn primary" disabled={resetPassword.length < 8}>
                Set new password
              </button>
            </div>
          </form>
        </div>
      )}

      {scopeTarget && (
        <div className="panel" style={{ marginTop: "1.1rem" }}>
          <h3>
            Scopes for {scopeTarget.username}
            <button className="close-detail" onClick={() => setScopeTarget(null)}>✕</button>
          </h3>
          <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
            No scopes selected means unrestricted (sees the whole fleet) — this is the default for every operator. A
            region scope covers every customer under it automatically.
          </p>
          <div className="form-row" style={{ marginTop: 0 }}>
            <label className="dim" style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.82rem" }}>
              <input type="checkbox" checked={globalAccess} onChange={(e) => setGlobalAccess(e.target.checked)} />
              Explicit global access — fleet-wide regardless of scopes below (audit P0.1: never inferred from an empty scope list)
            </label>
            <button className="btn" onClick={saveGlobalAccess}>
              Save global access
            </button>
          </div>
          <div className="split two-col" style={{ marginTop: 0 }}>
            <div>
              <h4 style={{ fontSize: "0.78rem", color: "var(--ink-faint)", margin: "0 0 0.4rem" }}>Regions</h4>
              {regions.length === 0 ? (
                <p className="dim" style={{ fontSize: "0.8rem" }}>None defined.</p>
              ) : (
                regions.map((r) => (
                  <label key={r.id} style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.82rem", padding: "0.2rem 0" }}>
                    <input type="checkbox" checked={scopeSelection.has(`region:${r.id}`)} onChange={() => toggleScope(`region:${r.id}`)} />
                    {r.name}
                  </label>
                ))
              )}
            </div>
            <div>
              <h4 style={{ fontSize: "0.78rem", color: "var(--ink-faint)", margin: "0 0 0.4rem" }}>Customers</h4>
              {customers.length === 0 ? (
                <p className="dim" style={{ fontSize: "0.8rem" }}>None defined.</p>
              ) : (
                customers.map((c) => (
                  <label key={c.id} style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.82rem", padding: "0.2rem 0" }}>
                    <input type="checkbox" checked={scopeSelection.has(`customer:${c.id}`)} onChange={() => toggleScope(`customer:${c.id}`)} />
                    {c.name}
                  </label>
                ))
              )}
            </div>
          </div>
          <button className="btn primary" style={{ marginTop: "0.75rem" }} onClick={saveScopes}>
            Save scopes
          </button>
        </div>
      )}

      {loading && items.length === 0 ? (
        <div className="loading">Loading operators…</div>
      ) : (
        <div style={{ marginTop: "1.1rem" }}>
          <DataTable data={items} columns={columns} getRowId={(o) => o.id} emptyMessage="No operators yet." />
        </div>
      )}

      {matrix && (
        <div className="panel" style={{ marginTop: "1.1rem", overflowX: "auto" }}>
          <h3>Role permissions</h3>
          <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
            superadmin always has every permission (not shown/configurable). Toggling here takes effect immediately —
            these are enforced on the server, this UI is not the security boundary.
          </p>
          <table style={{ fontFamily: "var(--font-ui)" }}>
            <thead>
              <tr>
                <th>Capability</th>
                {matrix.roles.map((r) => (
                  <th key={r} style={{ textAlign: "center" }}>{r}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {matrix.permissions.map((perm) => (
                <tr key={perm}>
                  <td>{PERMISSION_LABEL[perm]}</td>
                  {matrix.roles.map((r) => {
                    const granted = matrix.matrix[r]?.[perm] ?? false;
                    return (
                      <td key={r} style={{ textAlign: "center" }}>
                        <input
                          type="checkbox"
                          aria-label={`${PERMISSION_LABEL[perm]} — ${r}`}
                          checked={granted}
                          onChange={() => togglePermission(r, perm, granted)}
                        />
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
