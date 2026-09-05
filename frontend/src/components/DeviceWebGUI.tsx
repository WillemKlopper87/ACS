// Device web-GUI embed (admin-platform backlog, scaffolded "build now,
// functional later"). The iframe points at cmd/api's reverse proxy
// (api.webGUIProxyURL), not the device directly — the browser can only
// ever reach the ACS, same reachability model as the SSH/Telnet bridge.
// Real caveat, shown in the panel itself: some device UIs set
// X-Frame-Options/CSP headers of their own that block iframe embedding
// regardless of the proxy — "Open in new tab" is the reliable fallback.
import { useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { WebGUIConfig } from "../api/types";
import { toast } from "../lib/toast";

export function DeviceWebGUI({ id, writable }: { id: string; writable: boolean }) {
  const [config, setConfig] = useState<WebGUIConfig | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState({ base_url: "", username: "", password: "" });
  const [showFrame, setShowFrame] = useState(false);
  // The iframe URL carries a short-lived browser ticket (audit P1.4),
  // minted when the frame is opened rather than baked into the render.
  const [frameSrc, setFrameSrc] = useState<string | null>(null);

  useEffect(() => {
    if (!showFrame) {
      setFrameSrc(null);
      return;
    }
    let cancelled = false;
    api
      .webGUIProxyURL(id)
      .then((url) => {
        if (!cancelled) setFrameSrc(url);
      })
      .catch((e) => toast(e instanceof ApiError ? e.message : "Failed to obtain a web GUI ticket", "error"));
    return () => {
      cancelled = true;
    };
  }, [showFrame, id]);

  async function load() {
    try {
      const cfg = await api.getWebGUIConfig(id);
      setConfig(cfg);
      if (cfg.base_url) setForm({ base_url: cfg.base_url, username: cfg.username ?? "", password: "" });
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to load web GUI config", "error");
    }
  }

  useEffect(() => {
    load();
    setShowFrame(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  async function handleSave() {
    if (!form.base_url.trim()) {
      toast("Base URL is required", "error");
      return;
    }
    try {
      await api.setWebGUIConfig(id, form);
      setShowForm(false);
      await load();
      toast("Web GUI configured", "success");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to save web GUI config", "error");
    }
  }

  async function handleDelete() {
    if (!window.confirm("Remove this device's web GUI configuration?")) return;
    try {
      await api.deleteWebGUIConfig(id);
      setConfig({ configured: false });
      setShowFrame(false);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to remove web GUI config", "error");
    }
  }

  const configured = !!config?.base_url;

  return (
    <div className="panel">
      <h3>
        Device web GUI
        <button className="btn sm" onClick={() => setShowForm((s) => !s)} disabled={!writable}>
          {showForm ? "Cancel" : configured ? "Edit" : "Configure"}
        </button>
      </h3>

      {showForm && (
        <div className="form-row" style={{ marginTop: 0, flexWrap: "wrap" }}>
          <input aria-label="Base URL, e.g. http://192.168.1.1" placeholder="Base URL, e.g. http://192.168.1.1" value={form.base_url} onChange={(e) => setForm({ ...form, base_url: e.target.value })} style={{ minWidth: "14rem" }} />
          <input aria-label="Username (optional)" placeholder="Username (optional)" value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} />
          <input aria-label="Password (optional)" type="password" placeholder="Password (optional)" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
          <button className="btn primary" onClick={handleSave}>
            Save
          </button>
        </div>
      )}

      {!configured ? (
        <p style={{ color: "var(--ink-faint)", fontSize: "0.84rem", margin: "0.5rem 0 0" }}>
          No web GUI configured for this device yet.
        </p>
      ) : (
        <>
          <div className="form-row" style={{ marginTop: showForm ? "0.75rem" : 0 }}>
            <span className="dim" style={{ fontFamily: "var(--font-mono)", fontSize: "0.8rem", flex: 1 }}>
              {config?.base_url}
            </span>
            <button className="btn primary" onClick={() => setShowFrame((s) => !s)}>
              {showFrame ? "Hide" : "Open embedded"}
            </button>
            <a className="btn" href={config?.base_url} target="_blank" rel="noreferrer">
              Open in new tab
            </a>
            <button className="btn" onClick={handleDelete} disabled={!writable}>
              Remove
            </button>
          </div>

          {showFrame && frameSrc && (
            <iframe
              src={frameSrc}
              title="Device web GUI"
              style={{
                marginTop: "0.6rem",
                width: "100%",
                height: "24rem",
                border: "1px solid var(--border)",
                borderRadius: "var(--radius-md, 6px)",
                background: "#fff",
              }}
            />
          )}
        </>
      )}

      <p style={{ color: "var(--ink-faint)", fontSize: "0.72rem", marginTop: "0.6rem", marginBottom: 0 }}>
        Requests are proxied through the ACS (the browser never talks to the device directly) — but a CGNAT'd device
        is still unreachable from the ACS itself until a VPN/tunnel path exists. Some device UIs also refuse iframe
        embedding outright; "Open in new tab" always works if the device is reachable at all.
      </p>
    </div>
  );
}
