import { useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Region, Customer, Project, ImportResult } from "../api/types";
import { toast } from "../lib/toast";

// Multi-tenancy management (admin-platform backlog): regions/customers/
// projects are the org chart (superadmin-only structural CRUD, enforced
// server-side — this screen just reflects that), plus bulk device import.
// Device-to-customer/project assignment happens per-device in Device
// Detail, not here — this screen is the structure, not the assignments.
export function Tenancy() {
  const [regions, setRegions] = useState<Region[]>([]);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState<string | null>(null);

  const [regionName, setRegionName] = useState("");
  const [customerName, setCustomerName] = useState("");
  const [customerRegion, setCustomerRegion] = useState("");
  const [projectName, setProjectName] = useState("");
  const [projectDesc, setProjectDesc] = useState("");

  const [importFormat, setImportFormat] = useState<"json" | "csv" | "xml">("csv");
  const [importBody, setImportBody] = useState(
    "manufacturer,oui,product_class,serial_number,customer_id,tags\nExampleVendor,AABBCC,ExampleModel,SN0001,,edge;canary",
  );
  const [importResult, setImportResult] = useState<ImportResult | null>(null);
  const [importing, setImporting] = useState(false);

  async function load() {
    try {
      const [r, c, p] = await Promise.all([api.listRegions(), api.listCustomers(), api.listProjects()]);
      setRegions(r.items);
      setCustomers(c.items);
      setProjects(p.items);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API");
    }
  }

  useEffect(() => {
    load();
  }, []);

  const regionName_ = (id?: string) => regions.find((r) => r.id === id)?.name ?? "—";

  async function createRegion(e: React.FormEvent) {
    e.preventDefault();
    try {
      await api.createRegion(regionName);
      setRegionName("");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to create region", "error");
    }
  }

  async function createCustomer(e: React.FormEvent) {
    e.preventDefault();
    try {
      await api.createCustomer(customerName, customerRegion || undefined);
      setCustomerName("");
      setCustomerRegion("");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to create customer", "error");
    }
  }

  async function createProject(e: React.FormEvent) {
    e.preventDefault();
    try {
      await api.createProject(projectName, projectDesc || undefined);
      setProjectName("");
      setProjectDesc("");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to create project", "error");
    }
  }

  // Structural deletes are destructive and the server can legitimately
  // refuse one (a customer that still owns devices trips a foreign key),
  // so they confirm first and surface the outcome. These were previously
  // bare `await api.deleteX(id); load()` click handlers: a rejection
  // became an unhandled promise rejection, load() never ran, and the
  // operator saw the row sit there with no indication anything failed.
  async function remove(kind: string, name: string, del: () => Promise<void>) {
    if (!window.confirm(`Delete ${kind} "${name}"? This cannot be undone.`)) return;
    try {
      await del();
      toast(`Deleted ${kind} "${name}"`, "info");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : `Failed to delete ${kind}`, "error");
    }
  }

  async function runImport(e: React.FormEvent) {
    e.preventDefault();
    setImporting(true);
    setImportResult(null);
    try {
      const res = await api.importDevices(importFormat, importBody);
      setImportResult(res);
      toast(`Imported ${res.succeeded} of ${res.total_rows} devices`, res.failed > 0 ? "error" : "success");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Import failed", "error");
    } finally {
      setImporting(false);
    }
  }

  return (
    <section>
      {error && <div className="banner error">{error}</div>}

      <div className="split two-col">
        <div className="panel">
          <h3>Regions</h3>
          <form onSubmit={createRegion} className="form-row" style={{ marginTop: 0 }}>
            <input placeholder="Region name" value={regionName} onChange={(e) => setRegionName(e.target.value)} required />
            <button type="submit" className="btn primary">Add</button>
          </form>
          {regions.length === 0 ? (
            <p className="dim" style={{ fontSize: "0.84rem" }}>No regions yet.</p>
          ) : (
            regions.map((r) => (
              <div className="param-row" key={r.id}>
                <span className="path">{r.name}</span>
                <button className="btn danger" style={{ padding: "0.15em 0.5em", fontSize: "0.72rem" }} onClick={() => remove("region", r.name, () => api.deleteRegion(r.id))}>
                  Delete
                </button>
              </div>
            ))
          )}
        </div>

        <div className="panel">
          <h3>Customers (ISPs)</h3>
          <form onSubmit={createCustomer} className="form-row" style={{ marginTop: 0, flexWrap: "wrap" }}>
            <input placeholder="Customer name" value={customerName} onChange={(e) => setCustomerName(e.target.value)} required />
            <select className="chip" aria-label="Region" value={customerRegion} onChange={(e) => setCustomerRegion(e.target.value)}>
              <option value="">No region</option>
              {regions.map((r) => (
                <option key={r.id} value={r.id}>{r.name}</option>
              ))}
            </select>
            <button type="submit" className="btn primary">Add</button>
          </form>
          {customers.length === 0 ? (
            <p className="dim" style={{ fontSize: "0.84rem" }}>No customers yet.</p>
          ) : (
            customers.map((c) => (
              <div className="param-row" key={c.id}>
                <span className="path">{c.name} <span className="dim">· {regionName_(c.region_id)}</span></span>
                <button className="btn danger" style={{ padding: "0.15em 0.5em", fontSize: "0.72rem" }} onClick={() => remove("customer", c.name, () => api.deleteCustomer(c.id))}>
                  Delete
                </button>
              </div>
            ))
          )}
        </div>

        <div className="panel">
          <h3>Projects</h3>
          <p className="dim" style={{ marginTop: 0, fontSize: "0.8rem" }}>
            Cross-cutting tags — a device can belong to several, independent of its customer/region.
          </p>
          <form onSubmit={createProject} className="form-row" style={{ marginTop: 0, flexWrap: "wrap" }}>
            <input placeholder="Project name" value={projectName} onChange={(e) => setProjectName(e.target.value)} required />
            <input placeholder="Description (optional)" value={projectDesc} onChange={(e) => setProjectDesc(e.target.value)} />
            <button type="submit" className="btn primary">Add</button>
          </form>
          {projects.length === 0 ? (
            <p className="dim" style={{ fontSize: "0.84rem" }}>No projects yet.</p>
          ) : (
            projects.map((p) => (
              <div className="param-row" key={p.id}>
                <span className="path">{p.name}<span className="src">{p.description}</span></span>
                <button className="btn danger" style={{ padding: "0.15em 0.5em", fontSize: "0.72rem" }} onClick={() => remove("project", p.name, () => api.deleteProject(p.id))}>
                  Delete
                </button>
              </div>
            ))
          )}
        </div>

        <div className="panel">
          <h3>Bulk device import</h3>
          <p className="dim" style={{ marginTop: 0, fontSize: "0.8rem" }}>
            Pre-registers devices ahead of their first check-in — required columns/fields: manufacturer, oui,
            product_class, serial_number; customer_id and tags are optional. When the real device later Informs, it
            enriches this same row rather than creating a duplicate.
          </p>
          <form onSubmit={runImport}>
            <div className="form-row" style={{ marginTop: 0 }}>
              <select className="chip" aria-label="Import format" value={importFormat} onChange={(e) => setImportFormat(e.target.value as any)}>
                <option value="csv">CSV</option>
                <option value="json">JSON</option>
                <option value="xml">XML</option>
              </select>
              <button type="submit" className="btn primary" disabled={importing}>
                {importing ? "Importing…" : "Import"}
              </button>
            </div>
            <textarea
              value={importBody}
              onChange={(e) => setImportBody(e.target.value)}
              rows={6}
              style={{
                width: "100%", marginTop: "0.5rem", background: "var(--surface-2)", border: "1px solid var(--border)",
                borderRadius: "var(--radius-md, 6px)", padding: "0.5rem 0.6rem", color: "var(--ink)",
                fontFamily: "var(--font-mono)", fontSize: "0.78rem", resize: "vertical",
              }}
            />
          </form>
          {importResult && (
            <div style={{ marginTop: "0.75rem" }}>
              <p style={{ fontSize: "0.82rem" }}>
                {importResult.succeeded} of {importResult.total_rows} succeeded
                {importResult.failed > 0 && <span style={{ color: "var(--danger)" }}> — {importResult.failed} failed</span>}
              </p>
              {importResult.results.filter((r) => r.status === "error").map((r, i) => (
                <div key={i} className="param-row">
                  <span className="path">{r.serial_number || "(no serial)"}</span>
                  <span style={{ color: "var(--danger)" }}>{r.error}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
