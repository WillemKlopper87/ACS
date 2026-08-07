import { useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Customer, Project, Region } from "../api/types";
import { toast } from "../lib/toast";

// Excel reporting (admin-platform backlog): device status/firmware/SSID/
// location/identity, exportable to a real .xlsx, filterable to a region/
// customer/project on top of the operator's own multi-tenancy scope
// (always applied server-side — an ISP-scoped operator's export can never
// include another customer's devices, filter or no filter).
export function Reports() {
  const [regions, setRegions] = useState<Region[]>([]);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [regionId, setRegionId] = useState("");
  const [customerId, setCustomerId] = useState("");
  const [projectId, setProjectId] = useState("");
  const [exporting, setExporting] = useState(false);

  useEffect(() => {
    Promise.all([api.listRegions(), api.listCustomers(), api.listProjects()])
      .then(([r, c, p]) => {
        setRegions(r.items);
        setCustomers(c.items);
        setProjects(p.items);
      })
      .catch((e) => toast(e instanceof ApiError ? e.message : "Failed to load filters", "error"));
  }, []);

  async function handleExport() {
    setExporting(true);
    try {
      await api.exportDevicesReport({ region_id: regionId || undefined, customer_id: customerId || undefined, project_id: projectId || undefined });
      toast("Report downloaded", "success");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Export failed", "error");
    } finally {
      setExporting(false);
    }
  }

  return (
    <section>
      <div className="panel" style={{ maxWidth: "40rem" }}>
        <h3>Device report</h3>
        <p className="dim" style={{ marginTop: 0, fontSize: "0.84rem" }}>
          Exports an Excel (.xlsx) sheet: serial, manufacturer, model, MAC address, online status, firmware version,
          current SSID, location, customer, and region — for the whole fleet, or narrowed to one region/customer/
          project below. If your account is scoped to specific customers, the export never includes devices outside
          that scope, regardless of these filters.
        </p>

        <div className="form-row" style={{ marginTop: 0, flexWrap: "wrap" }}>
          <select className="chip" value={regionId} onChange={(e) => setRegionId(e.target.value)}>
            <option value="">All regions</option>
            {regions.map((r) => (
              <option key={r.id} value={r.id}>{r.name}</option>
            ))}
          </select>
          <select className="chip" value={customerId} onChange={(e) => setCustomerId(e.target.value)}>
            <option value="">All customers</option>
            {customers.map((c) => (
              <option key={c.id} value={c.id}>{c.name}</option>
            ))}
          </select>
          <select className="chip" value={projectId} onChange={(e) => setProjectId(e.target.value)}>
            <option value="">All projects</option>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>{p.name}</option>
            ))}
          </select>
        </div>

        <div className="form-row">
          <button className="btn primary" onClick={handleExport} disabled={exporting}>
            {exporting ? "Exporting…" : "Export to Excel"}
          </button>
        </div>
      </div>
    </section>
  );
}
