import type { ColumnDef } from "@tanstack/react-table";
import { useEffect, useMemo, useState } from "react";
import { api, ApiError } from "../api/client";
import type { FirmwareImage, Rollout, RolloutDetail } from "../api/types";
import { DataTable } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { fmtTime, timeAgo } from "../lib/format";
import { useAuth } from "../auth/useAuth";
import { canWrite } from "../auth/roles";
import { toast } from "../lib/toast";
import { useEscape } from "../lib/hotkeys";
import { useCustomers, customerName } from "../lib/useCustomers";

export function FirmwareRollouts() {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [items, setItems] = useState<Rollout[]>([]);
  const [images, setImages] = useState<FirmwareImage[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<RolloutDetail | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [imageId, setImageId] = useState("");
  const [rollbackImageId, setRollbackImageId] = useState("");
  const [modelFilter, setModelFilter] = useState("");
  const [canaryPct, setCanaryPct] = useState(10);
  const [maxFailureRate, setMaxFailureRate] = useState(0.2);
  const [customerId, setCustomerId] = useState("");
  const [createError, setCreateError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const customers = useCustomers();

  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [uploadVendor, setUploadVendor] = useState("");
  const [uploadModel, setUploadModel] = useState("");
  const [uploadVersion, setUploadVersion] = useState("");
  const [uploadChannel, setUploadChannel] = useState("stable");
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);

  async function load() {
    setLoading(true);
    setError(null);
    try {
      const [rolloutsRes, imagesRes] = await Promise.all([api.listRollouts(), api.listFirmwareImages()]);
      setItems(rolloutsRes.items);
      setImages(imagesRes.items);
      if (!imageId && imagesRes.items.length > 0) setImageId(imagesRes.items[0].id);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API — is cmd/api running?");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEscape(() => setSelected(null), selected !== null);

  async function openRollout(r: Rollout) {
    setActionError(null);
    try {
      setSelected(await api.getRollout(r.id));
    } catch {
      /* leave selection as-is on transient error */
    }
  }

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    setCreateError(null);
    setCreating(true);
    try {
      await api.createRollout({
        name,
        firmware_image_id: imageId,
        rollback_firmware_image_id: rollbackImageId || undefined,
        model_filter: modelFilter || undefined,
        canary_percentage: canaryPct,
        maximum_failure_rate: maxFailureRate,
        customer_id: customerId || null,
      });
      toast(`Rollout "${name}" created`, "success");
      setName("");
      setModelFilter("");
      setRollbackImageId("");
      setCustomerId("");
      await load();
    } catch (e) {
      setCreateError(e instanceof ApiError ? e.message : "Failed to create rollout");
    } finally {
      setCreating(false);
    }
  }

  async function onUpload(e: React.FormEvent) {
    e.preventDefault();
    if (!uploadFile) return;
    setUploadError(null);
    setUploading(true);
    try {
      const img = await api.uploadFirmwareImage(uploadFile, uploadVendor, uploadModel, uploadVersion, uploadChannel);
      toast(`Firmware image "${img.vendor} ${img.model} v${img.version}" uploaded`, "success");
      setUploadFile(null);
      setUploadVendor("");
      setUploadModel("");
      setUploadVersion("");
      setUploadChannel("stable");
      await load();
    } catch (e) {
      setUploadError(e instanceof ApiError ? e.message : "Failed to upload firmware image");
    } finally {
      setUploading(false);
    }
  }

  async function onStart(id: string) {
    setActionError(null);
    try {
      await api.startRollout(id);
      toast("Canary rollout started", "success");
      await load();
      const full = await api.getRollout(id);
      setSelected(full);
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : "Failed to start rollout";
      setActionError(msg);
      toast(msg, "error");
    }
  }

  async function onAdvance(id: string) {
    setActionError(null);
    try {
      const res = await api.advanceRollout(id);
      toast(
        res.final_wave
          ? `Final wave dispatched (${res.dispatched} devices) — rollout complete`
          : `Wave dispatched: ${res.dispatched} devices — more remain, advance again when ready`,
        "success",
      );
      await load();
      const full = await api.getRollout(id);
      setSelected(full);
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : "Failed to advance rollout";
      setActionError(msg);
      toast(msg, "error");
    }
  }

  const columns = useMemo<ColumnDef<Rollout, any>[]>(
    () => [
      { accessorKey: "name", header: "Name" },
      { accessorKey: "customer_id", header: "Customer", cell: ({ getValue }) => <span className="dim">{customerName(customers, getValue() as string | null)}</span> },
      { accessorKey: "canary_percentage", header: "Canary %", cell: ({ getValue }) => <span className="dim">{getValue() as number}%</span> },
      { accessorKey: "maximum_failure_rate", header: "Max Failure", cell: ({ getValue }) => <span className="dim">{Math.round((getValue() as number) * 100)}%</span> },
      { accessorKey: "status", header: "Status", cell: ({ getValue }) => <StatusBadge value={getValue() as string} /> },
      {
        accessorKey: "created_at",
        header: "Created",
        cell: ({ getValue }) => {
          const v = getValue() as string;
          return <span className="dim" title={`${fmtTime(v)} UTC`}>{timeAgo(v)}</span>;
        },
      },
    ],
    [customers],
  );

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" onClick={load}>Retry</button>
        </div>
      )}

      <div className="panel">
        <h3>Upload firmware image</h3>
        <form onSubmit={onUpload}>
          <div className="form-row">
            <input
              type="file"
              onChange={(e) => setUploadFile(e.target.files?.[0] ?? null)}
              disabled={uploading || !writable}
              style={{ flex: 1, fontFamily: "var(--font-mono)", fontSize: "0.8rem" }}
            />
          </div>
          <div className="form-row">
            <label className="field">
              <span>Vendor</span>
              <input value={uploadVendor} onChange={(e) => setUploadVendor(e.target.value)} disabled={uploading || !writable} required />
            </label>
            <label className="field">
              <span>Model</span>
              <input value={uploadModel} onChange={(e) => setUploadModel(e.target.value)} disabled={uploading || !writable} required />
            </label>
            <label className="field">
              <span>Version</span>
              <input value={uploadVersion} onChange={(e) => setUploadVersion(e.target.value)} disabled={uploading || !writable} required />
            </label>
            <label className="field">
              <span>Release channel</span>
              <select value={uploadChannel} onChange={(e) => setUploadChannel(e.target.value)} disabled={uploading || !writable}>
                <option value="stable">stable</option>
                <option value="beta">beta</option>
                <option value="canary">canary</option>
              </select>
            </label>
          </div>
          {uploadError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{uploadError}</div>}
          <div className="form-row">
            <button type="submit" className="btn primary" disabled={uploading || !writable || !uploadFile || !uploadVendor || !uploadModel || !uploadVersion}>
              {uploading ? "Uploading…" : "Upload image"}
            </button>
          </div>
        </form>
      </div>

      <div className="split two-col">
        <div className="panel">
          <h3>Create canary rollout</h3>
          <form onSubmit={onCreate}>
            <div className="form-row">
              <label className="field">
                <span>Name</span>
                <input value={name} onChange={(e) => setName(e.target.value)} required />
              </label>
              <label className="field">
                <span>Firmware image</span>
                <select value={imageId} onChange={(e) => setImageId(e.target.value)} required>
                  {images.length === 0 && <option value="">No firmware images</option>}
                  {images.map((img) => (
                    <option key={img.id} value={img.id}>
                      {img.vendor} {img.model} v{img.version}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            <div className="form-row">
              <label className="dim" style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.8rem", flex: 1 }}>
                Rollback image (optional)
                <select className="chip" aria-label="Rollback image" value={rollbackImageId} onChange={(e) => setRollbackImageId(e.target.value)} style={{ flex: 1 }}>
                  <option value="">None — no automatic rollback if blocked</option>
                  {images.map((img) => (
                    <option key={img.id} value={img.id}>
                      {img.vendor} {img.model} v{img.version}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            <div className="form-row">
              <label className="field">
                <span>Model filter (optional)</span>
                <input placeholder="manufacturer or product class" value={modelFilter} onChange={(e) => setModelFilter(e.target.value)} />
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
            <div className="form-row">
              <label className="dim" style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.8rem" }}>
                Canary %
                <input type="number" min={1} max={100} value={canaryPct} onChange={(e) => setCanaryPct(Number(e.target.value))} style={{ maxWidth: "5rem" }} />
              </label>
              <label className="dim" style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.8rem" }}>
                Max failure rate
                <input
                  type="number"
                  min={0}
                  max={1}
                  step={0.05}
                  value={maxFailureRate}
                  onChange={(e) => setMaxFailureRate(Number(e.target.value))}
                  style={{ maxWidth: "5rem" }}
                />
              </label>
            </div>
            {createError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{createError}</div>}
            <div className="form-row">
              <button type="submit" className="btn primary" disabled={creating || !writable || !name || !imageId}>
                {creating ? "Creating…" : "Create rollout"}
              </button>
            </div>
          </form>
        </div>

        <div className="panel">
          <h3>
            {selected ? selected.name : "Select a rollout"}
            {selected && (
              <button className="close-detail" onClick={() => setSelected(null)}>
                ✕
              </button>
            )}
          </h3>
          {selected ? (
            <>
              <dl className="kv">
                <dt>Status</dt>
                <dd><StatusBadge value={selected.status} /></dd>
                <dt>Failure rate</dt>
                <dd>{Math.round(selected.failure_rate * 100)}% ({selected.terminal_count} terminal)</dd>
                <dt>State counts</dt>
                <dd>{Object.entries(selected.state_counts).map(([k, v]) => `${k}: ${v}`).join(", ") || "—"}</dd>
                {selected.rollback_firmware_image_id && (
                  <>
                    <dt>Rollback image</dt>
                    <dd>
                      {images.find((img) => img.id === selected.rollback_firmware_image_id)?.version ?? selected.rollback_firmware_image_id}
                      {selected.rollback_dispatched_at && (
                        <span style={{ color: "var(--danger)" }}> — dispatched {timeAgo(selected.rollback_dispatched_at)}</span>
                      )}
                    </dd>
                  </>
                )}
              </dl>
              {actionError && <div className="banner error" style={{ marginTop: "0.6rem" }}>{actionError}</div>}
              <div className="form-row">
                <button className="btn primary" onClick={() => onStart(selected.id)} disabled={!writable || selected.status !== "DRAFT"}>
                  Start canary
                </button>
                <button className="btn" onClick={() => onAdvance(selected.id)} disabled={!writable || selected.status !== "ACTIVE"} title="Dispatches one more wave, sized the same as the canary batch">
                  Advance next wave
                </button>
              </div>
              <ul style={{ listStyle: "none", margin: "0.75rem 0 0", padding: 0, maxHeight: "14rem", overflow: "auto" }}>
                {selected.devices.map((d) => (
                  <li
                    key={d.device_id}
                    style={{
                      display: "flex",
                      justifyContent: "space-between",
                      fontFamily: "var(--font-mono)",
                      fontSize: "0.78rem",
                      padding: "0.3rem 0",
                      borderBottom: "1px solid var(--border)",
                    }}
                  >
                    <span>{d.oui_serial}</span>
                    <StatusBadge value={d.state} />
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <p className="dim" style={{ margin: 0 }}>
              Click a rollout in the table below for status and controls.
            </p>
          )}
        </div>
      </div>

      {loading && items.length === 0 ? (
        <div className="loading">Loading rollouts…</div>
      ) : (
        <div style={{ marginTop: "1.1rem" }}>
          <DataTable data={items} columns={columns} getRowId={(r) => r.id} onRowClick={openRollout} emptyMessage="No firmware rollouts yet." />
        </div>
      )}
    </section>
  );
}
