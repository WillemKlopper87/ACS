// Device tenancy assignment (admin-platform backlog: multi-tenancy) —
// which customer owns this device, and which projects it's tagged with.
// Structural regions/customers/projects are managed on the Tenancy screen;
// this is just the per-device assignment, gated server-side by the
// tenancy.manage curated permission (not superadmin-only, unlike creating
// the customers/projects themselves).
import { useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Customer, Project } from "../api/types";
import { toast } from "../lib/toast";

export function DeviceTenancy({ deviceId, customerId, writable }: { deviceId: string; customerId?: string; writable: boolean }) {
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [allProjects, setAllProjects] = useState<Project[]>([]);
  const [deviceProjects, setDeviceProjects] = useState<Project[]>([]);
  const [selectedCustomer, setSelectedCustomer] = useState(customerId ?? "");
  const [selectedProjectIds, setSelectedProjectIds] = useState<Set<string>>(new Set());

  async function load() {
    try {
      const [c, p, dp] = await Promise.all([api.listCustomers(), api.listProjects(), api.getDeviceProjects(deviceId)]);
      setCustomers(c.items);
      setAllProjects(p.items);
      setDeviceProjects(dp.items);
      setSelectedProjectIds(new Set(dp.items.map((x) => x.id)));
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to load tenancy data", "error");
    }
  }

  useEffect(() => {
    load();
    setSelectedCustomer(customerId ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deviceId, customerId]);

  async function saveCustomer() {
    try {
      await api.assignDeviceCustomer(deviceId, selectedCustomer || null);
      toast("Customer assignment saved", "success");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to assign customer", "error");
    }
  }

  function toggleProject(id: string) {
    setSelectedProjectIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function saveProjects() {
    try {
      await api.setDeviceProjects(deviceId, Array.from(selectedProjectIds));
      toast("Projects saved", "success");
      const dp = await api.getDeviceProjects(deviceId);
      setDeviceProjects(dp.items);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to save projects", "error");
    }
  }

  return (
    <div className="panel">
      <h3>Tenancy</h3>
      <div className="form-row" style={{ marginTop: 0 }}>
        <select className="chip" value={selectedCustomer} onChange={(e) => setSelectedCustomer(e.target.value)} disabled={!writable}>
          <option value="">Unassigned</option>
          {customers.map((c) => (
            <option key={c.id} value={c.id}>{c.name}</option>
          ))}
        </select>
        <button className="btn" onClick={saveCustomer} disabled={!writable}>
          Save customer
        </button>
      </div>

      {allProjects.length === 0 ? (
        <p className="dim" style={{ fontSize: "0.8rem", marginTop: "0.6rem" }}>No projects defined yet — create some on the Tenancy screen.</p>
      ) : (
        <div style={{ marginTop: "0.6rem" }}>
          {allProjects.map((p) => (
            <label key={p.id} style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.82rem", padding: "0.2rem 0" }}>
              <input type="checkbox" checked={selectedProjectIds.has(p.id)} onChange={() => toggleProject(p.id)} disabled={!writable} />
              {p.name}
            </label>
          ))}
          <button className="btn" style={{ marginTop: "0.4rem" }} onClick={saveProjects} disabled={!writable}>
            Save projects
          </button>
        </div>
      )}

      {deviceProjects.length > 0 && (
        <p className="dim" style={{ fontSize: "0.76rem", marginTop: "0.6rem", marginBottom: 0 }}>
          Currently: {deviceProjects.map((p) => p.name).join(", ")}
        </p>
      )}
    </div>
  );
}
