import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Credential, Device, Job, ParameterCache, ParameterHistoryEntry } from "../api/types";
import { StatusBadge } from "../components/StatusBadge";
import { DeviceConsole } from "../components/DeviceConsole";
import { RemoteShell } from "../components/RemoteShell";
import { DeviceWebGUI } from "../components/DeviceWebGUI";
import { VPNTunnel } from "../components/VPNTunnel";
import { DeviceTenancy } from "../components/DeviceTenancy";
import { timeAgo } from "../lib/format";
import { useAuth } from "../auth/useAuth";
import { canWrite } from "../auth/roles";
import { useLive } from "../lib/useLive";
import { useEscape } from "../lib/hotkeys";
import { toast } from "../lib/toast";

export function DeviceDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [device, setDevice] = useState<Device | null>(null);
  const [params, setParams] = useState<ParameterCache>({});
  const [jobs, setJobs] = useState<Job[]>([]);
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(busy);
  useEffect(() => {
    busyRef.current = busy;
  }, [busy]);
  const [ssidInput, setSsidInput] = useState("");
  const [pingHost, setPingHost] = useState("");
  const [tracerouteHost, setTracerouteHost] = useState("");
  const [objectPath, setObjectPath] = useState("");
  const [scheduleDelay, setScheduleDelay] = useState(3600);
  const [attrSetPath, setAttrSetPath] = useState("");
  const [attrNotification, setAttrNotification] = useState(2);
  const [attrGetPaths, setAttrGetPaths] = useState("");
  const [historyPath, setHistoryPath] = useState<string | null>(null);
  const [historyEntries, setHistoryEntries] = useState<ParameterHistoryEntry[]>([]);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [tagsInput, setTagsInput] = useState("");
  const [tagsDirty, setTagsDirty] = useState(false);
  const tagsDirtyRef = useRef(tagsDirty);
  useEffect(() => {
    tagsDirtyRef.current = tagsDirty;
  }, [tagsDirty]);
  const [locationInput, setLocationInput] = useState("");
  const [locationDirty, setLocationDirty] = useState(false);
  const locationDirtyRef = useRef(locationDirty);
  useEffect(() => {
    locationDirtyRef.current = locationDirty;
  }, [locationDirty]);

  const load = useCallback(
    async (background = false) => {
      if (background && busyRef.current) return;
      if (!background) setError(null);
      try {
        const [d, p, j, c] = await Promise.all([
          api.getDevice(id),
          api.getDeviceParameters(id),
          api.listDeviceJobs(id),
          api.listDeviceCredentials(id),
        ]);
        setDevice(d);
        setParams(p.parameters);
        setJobs(j.items);
        setCredentials(c.items);
        if (!tagsDirtyRef.current) setTagsInput((d.tags ?? []).join(", "));
        if (!locationDirtyRef.current) setLocationInput(d.location ?? "");
        setError(null);
      } catch (e) {
        if (!background) setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to load device detail");
      }
    },
    [id],
  );

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  const [live, setLive] = useLive(() => load(true), 5000);
  useEscape(onClose, true);

  async function withBusy(fn: () => Promise<string>) {
    setBusy(true);
    try {
      const msg = await fn();
      toast(msg, "success");
      await load();
    } catch (e) {
      toast(e instanceof ApiError ? `${e.status}: ${e.message}` : "Action failed", "error");
    } finally {
      setBusy(false);
    }
  }

  const handleConnectionRequest = () =>
    withBusy(async () => {
      const res = await api.createConnectionRequest(id, 30);
      return `Queued ${res.command_key} (${res.reachability.connection_request_mode}: ${res.reachability.recommendation})`;
    });

  const handleRefreshCellular = () =>
    withBusy(async () => {
      const res = await api.refreshCellularDiagnostics(id);
      return `Queued ${res.command_key} — matched vendor ${res.matched_vendor || "(generic fallback)"}, ${res.parameters.length} parameters`;
    });

  const handleSetSsid = () =>
    withBusy(async () => {
      if (!ssidInput.trim()) throw new ApiError(400, "SSID cannot be empty");
      const res = await api.putDeviceParameters(id, [
        { name: "Device.WiFi.SSID.1.SSID", value: ssidInput.trim(), type: "string" },
      ]);
      setSsidInput("");
      return `Queued ${res.command_key} — applies on the device's next session`;
    });

  const handleSaveTags = () =>
    withBusy(async () => {
      const tags = tagsInput
        .split(",")
        .map((t) => t.trim())
        .filter(Boolean);
      await api.updateDeviceTags(id, tags);
      setTagsDirty(false);
      return `Tags saved: ${tags.length === 0 ? "(none)" : tags.join(", ")}`;
    });

  const handleSaveLocation = () =>
    withBusy(async () => {
      await api.updateDeviceLocation(id, locationInput.trim());
      setLocationDirty(false);
      return `Location saved: ${locationInput.trim() || "(none)"}`;
    });

  const handlePing = () =>
    withBusy(async () => {
      if (!pingHost.trim()) throw new ApiError(400, "Host cannot be empty");
      const res = await api.createDiagnosticsPing(id, pingHost.trim());
      return `Ping queued: ${res.command_key} — polls to completion, watch Recent jobs`;
    });

  const handleTraceroute = () =>
    withBusy(async () => {
      if (!tracerouteHost.trim()) throw new ApiError(400, "Host cannot be empty");
      const res = await api.createDiagnosticsTraceroute(id, tracerouteHost.trim());
      return `Traceroute queued: ${res.command_key} — polls to completion, watch Recent jobs`;
    });

  const handleRefreshWifiClients = () =>
    withBusy(async () => {
      const res = await api.refreshWifiClients(id);
      return `Queued ${res.command_key} — reading the WiFi AccessPoint subtree`;
    });

  const handleAddObject = () =>
    withBusy(async () => {
      const path = objectPath.trim();
      if (!path.endsWith(".")) throw new ApiError(400, 'Object path must end in "." (e.g. Device.WiFi.SSID.)');
      const res = await api.addObject(id, path);
      setObjectPath("");
      return `AddObject queued: ${res.command_key} — watch Recent jobs for the new instance number`;
    });

  const handleDeleteObject = () => {
    const path = objectPath.trim();
    if (!path.endsWith(".")) {
      toast('Object path must end in "." (e.g. Device.WiFi.SSID.3.)', "error");
      return;
    }
    if (!window.confirm(`Delete object instance "${path}" from this device? This cannot be undone.`)) return;
    withBusy(async () => {
      const res = await api.deleteObject(id, path);
      setObjectPath("");
      return `DeleteObject queued: ${res.command_key}`;
    });
  };

  const handleScheduleInform = () =>
    withBusy(async () => {
      const res = await api.scheduleInform(id, scheduleDelay);
      return `ScheduleInform queued: ${res.command_key} — device will check in again in ${scheduleDelay}s`;
    });

  const handleSetParameterAttribute = () =>
    withBusy(async () => {
      const path = attrSetPath.trim();
      if (!path) throw new ApiError(400, "Parameter path cannot be empty");
      const res = await api.setParameterAttributes(id, [{ name: path, notification: attrNotification }]);
      setAttrSetPath("");
      return `SetParameterAttributes queued: ${res.command_key} — notification level ${attrNotification} for ${path}`;
    });

  const handleGetParameterAttributes = () =>
    withBusy(async () => {
      const paths = attrGetPaths.split(",").map((p) => p.trim()).filter(Boolean);
      if (paths.length === 0) throw new ApiError(400, "At least one path is required");
      const res = await api.getParameterAttributes(id, paths);
      return `GetParameterAttributes queued: ${res.command_key} — watch Recent jobs for the result`;
    });

  const handleRequestUpload = (fileType: string) =>
    withBusy(async () => {
      const res = await api.createUpload(id, fileType);
      return `Upload requested: ${res.command_key} — file lands on receipt once the CPE pushes it`;
    });

  async function toggleHistory(path: string) {
    if (historyPath === path) {
      setHistoryPath(null);
      return;
    }
    setHistoryPath(path);
    setHistoryLoading(true);
    try {
      const res = await api.getParameterHistory(id, path);
      setHistoryEntries(res.items);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to load parameter history", "error");
      setHistoryEntries([]);
    } finally {
      setHistoryLoading(false);
    }
  }

  const handleReboot = () =>
    withBusy(async () => {
      const res = await api.rebootDevice(id);
      return `Reboot queued: ${res.command_key} — watch for a fresh Inform once it comes back up`;
    });

  const handleFactoryReset = () => {
    if (!window.confirm(`Factory reset ${device?.oui_serial}? This wipes the device back to defaults and cannot be undone.`)) return;
    withBusy(async () => {
      const res = await api.factoryResetDevice(id);
      return `Factory reset queued: ${res.command_key}`;
    });
  };

  const handleRotateCredential = () =>
    withBusy(async () => {
      const cred = await api.rotateDeviceCredential(id);
      return `Rotation queued: v${cred.version} (${cred.command_key}) — activate once the job shows SUCCESS`;
    });

  const handleActivateCredential = (credentialId: string) =>
    withBusy(async () => {
      const cred = await api.activateDeviceCredential(id, credentialId);
      return `v${cred.version} is now ACTIVE`;
    });

  const handleRevokeCredential = (credentialId: string) =>
    withBusy(async () => {
      const cred = await api.revokeDeviceCredential(id, credentialId);
      return `v${cred.version} REVOKED`;
    });

  if (error) {
    return (
      <div className="split">
        <div className="panel">
          <h3>
            Device detail
            <button className="close-detail" onClick={onClose} title="Close">✕</button>
          </h3>
          <div className="banner error">{error}</div>
        </div>
      </div>
    );
  }

  if (!device) {
    return <div className="loading">Loading device detail…</div>;
  }

  const paramEntries = Object.entries(params);

  return (
    <div className="split two-col">
      <div className="panel">
        <h3>
          Device detail — {device.oui_serial}
          <span style={{ display: "flex", alignItems: "center", gap: "0.6rem" }}>
            <button
              className={`btn live-toggle ${live ? "on" : ""}`}
              onClick={() => setLive((l) => !l)}
              style={{ padding: "0.2em 0.5em", fontSize: "0.72rem" }}
              title={live ? "Auto-refreshing every 5s — click to pause" : "Auto-refresh paused — click to resume"}
            >
              <span className="live-dot" /> {live ? "Live" : "Paused"}
            </button>
            <button className="close-detail" onClick={onClose} title="Close (Esc)">✕</button>
          </span>
        </h3>
        <dl className="kv">
          <dt>Manufacturer / Model</dt>
          <dd style={{ fontFamily: "var(--font-ui)" }}>
            {device.manufacturer} {device.product_class}
          </dd>
          <dt>Serial number</dt>
          <dd>{device.serial_number}</dd>
          <dt>Online status</dt>
          <dd><StatusBadge value={device.online_status} /></dd>
          <dt>Data model root</dt>
          <dd>{device.data_model_root}</dd>
          <dt>Connection Request URL</dt>
          <dd>{device.connection_request_url ?? "unknown — awaiting an Inform with this parameter"}</dd>
          <dt>Reachability mode</dt>
          <dd><StatusBadge value={device.connection_request_mode} /></dd>
          <dt>Last connection request</dt>
          <dd>
            {device.last_connection_request_status
              ? `${device.last_connection_request_status} · ${timeAgo(device.last_connection_request_at)}`
              : "never attempted"}
          </dd>
        </dl>

        <div style={{ marginTop: "1rem", display: "flex", gap: "0.5rem", flexWrap: "wrap" }}>
          <button className="btn primary" onClick={handleConnectionRequest} disabled={busy || !writable}>
            Queue Connection Request
          </button>
          <button className="btn" onClick={handleRefreshCellular} disabled={busy || !writable}>
            Refresh cellular diagnostics
          </button>
          <button className="btn" onClick={handleReboot} disabled={busy || !writable} title="Queues Reboot — the device drops and reconnects">
            Reboot
          </button>
          <button className="btn danger" onClick={handleFactoryReset} disabled={busy || !writable} title="Queues FactoryReset — wipes the device back to defaults">
            Factory reset
          </button>
        </div>

        <div className="form-row">
          <input
            placeholder="New WiFi SSID…"
            value={ssidInput}
            onChange={(e) => setSsidInput(e.target.value)}
            disabled={busy || !writable}
          />
          <button className="btn" onClick={handleSetSsid} disabled={busy || !writable || !ssidInput.trim()}>
            Set SSID
          </button>
        </div>

        <div className="form-row">
          <input
            type="number"
            min={60}
            value={scheduleDelay}
            onChange={(e) => setScheduleDelay(Number(e.target.value))}
            disabled={busy || !writable}
            style={{ maxWidth: "8rem" }}
          />
          <button
            className="btn"
            onClick={handleScheduleInform}
            disabled={busy || !writable || scheduleDelay < 1}
            title="Tells the device to Inform again after this many seconds, independent of its periodic interval"
          >
            Schedule Inform
          </button>
        </div>

        <div className="form-row">
          <input
            placeholder="Tags (comma-separated)"
            value={tagsInput}
            onChange={(e) => {
              setTagsInput(e.target.value);
              setTagsDirty(true);
            }}
            disabled={busy || !writable}
          />
          <button className="btn" onClick={handleSaveTags} disabled={busy || !writable}>
            Save tags
          </button>
        </div>

        <div className="form-row">
          <input
            placeholder="Location (e.g. Rack 4, POP-West)"
            value={locationInput}
            onChange={(e) => {
              setLocationInput(e.target.value);
              setLocationDirty(true);
            }}
            disabled={busy || !writable}
          />
          <button className="btn" onClick={handleSaveLocation} disabled={busy || !writable}>
            Save location
          </button>
        </div>
      </div>

      <div className="panel">
        <h3>Parameter cache{paramEntries.length === 0 ? " — empty" : ""}</h3>
        {paramEntries.length === 0 ? (
          <p style={{ color: "var(--ink-faint)", fontSize: "0.84rem", margin: 0 }}>
            No parameters read from this device yet — the cache populates from Inform and GET_PARAMETER jobs.
          </p>
        ) : (
          paramEntries.map(([path, v]) => (
            <div key={path}>
              <div className="param-row" style={{ cursor: "pointer" }} onClick={() => toggleHistory(path)} title="Click to view value history">
                <span className="path">{path}</span>
                <span className="val">{v.value}</span>
                <span className="src">
                  {v.source} · {timeAgo(v.updated_at)} · {historyPath === path ? "hide history ▲" : "history ▼"}
                </span>
              </div>
              {historyPath === path && (
                <div style={{ padding: "0.4rem 0 0.6rem 0.75rem", borderBottom: "1px solid var(--border)" }}>
                  {historyLoading ? (
                    <span className="dim" style={{ fontSize: "0.76rem" }}>Loading history…</span>
                  ) : historyEntries.length === 0 ? (
                    <span className="dim" style={{ fontSize: "0.76rem" }}>No recorded changes yet — history only records when a value actually changes.</span>
                  ) : (
                    historyEntries.map((entry, i) => (
                      <div key={i} style={{ display: "flex", justifyContent: "space-between", fontSize: "0.76rem", fontFamily: "var(--font-mono)", padding: "0.15rem 0" }}>
                        <span>{entry.value}</span>
                        <span className="dim">{entry.source} · {timeAgo(entry.recorded_at)}</span>
                      </div>
                    ))
                  )}
                </div>
              )}
            </div>
          ))
        )}

        <h3 style={{ marginTop: "1.4rem" }}>Recent jobs</h3>
        {jobs.length === 0 ? (
          <p style={{ color: "var(--ink-faint)", fontSize: "0.84rem", margin: 0 }}>No jobs queued for this device yet.</p>
        ) : (
          jobs.slice(0, 8).map((j) => (
            <div className="param-row" key={j.command_key}>
              <span className="path">
                {j.command_key} <span style={{ color: "var(--ink-faint)" }}>· {j.type}</span>
              </span>
              <span>
                <StatusBadge value={j.status} />
              </span>
              {j.fault_string && <span className="src">{j.fault_string}</span>}
              {j.result_detail?.instance_number !== undefined && (
                <span className="src">new instance: {j.result_detail.instance_number}</span>
              )}
              {j.type === "GET_PARAMETER_ATTRIBUTES" && j.result_detail && Object.keys(j.result_detail).length > 0 && (
                <span className="src">
                  {Object.entries(j.result_detail)
                    .map(([path, attr]) => `${path}=${(attr as { notification?: number })?.notification ?? "?"}`)
                    .join(", ")}
                </span>
              )}
            </div>
          ))
        )}
      </div>

      <div className="panel">
        <h3>
          Connection Request credentials
          <button className="btn" onClick={handleRotateCredential} disabled={busy || !writable}>
            Rotate
          </button>
        </h3>
        {credentials.length === 0 ? (
          <p style={{ color: "var(--ink-faint)", fontSize: "0.84rem", margin: 0 }}>
            No rotated credentials — this device still uses the shared ACS_CONNECTION_REQUEST_USERNAME/PASSWORD fallback.
          </p>
        ) : (
          credentials.map((c) => (
            <div className="param-row" key={c.id}>
              <span className="path">
                v{c.version} <span style={{ color: "var(--ink-faint)" }}>· {c.username}</span>
              </span>
              <span style={{ display: "flex", alignItems: "center", gap: "0.4rem" }}>
                <StatusBadge value={c.status} />
                {c.status === "PENDING" && (
                  <button className="btn" onClick={() => handleActivateCredential(c.id)} disabled={busy || !writable}>
                    Activate
                  </button>
                )}
                {(c.status === "GRACE" || c.status === "PENDING") && (
                  <button className="btn" onClick={() => handleRevokeCredential(c.id)} disabled={busy || !writable}>
                    Revoke
                  </button>
                )}
              </span>
            </div>
          ))
        )}
      </div>

      <div className="panel">
        <h3>Diagnostics</h3>
        <div className="form-row" style={{ marginTop: 0 }}>
          <input
            placeholder="Ping host (e.g. 8.8.8.8)"
            value={pingHost}
            onChange={(e) => setPingHost(e.target.value)}
            disabled={busy || !writable}
          />
          <button className="btn" onClick={handlePing} disabled={busy || !writable || !pingHost.trim()}>
            Ping
          </button>
        </div>
        <div className="form-row">
          <input
            placeholder="Traceroute host"
            value={tracerouteHost}
            onChange={(e) => setTracerouteHost(e.target.value)}
            disabled={busy || !writable}
          />
          <button className="btn" onClick={handleTraceroute} disabled={busy || !writable || !tracerouteHost.trim()}>
            Traceroute
          </button>
        </div>
        <div className="form-row">
          <button className="btn" onClick={handleRefreshWifiClients} disabled={busy || !writable} style={{ width: "100%", justifyContent: "center" }}>
            Refresh WiFi clients
          </button>
        </div>
        <p style={{ color: "var(--ink-faint)", fontSize: "0.76rem", marginTop: "0.6rem", marginBottom: 0 }}>
          Ping/Traceroute poll to completion — watch Recent jobs for the outcome; results land in the parameter cache above under Device.IP.Diagnostics.*.
        </p>
      </div>

      <div className="panel">
        <h3>Object management</h3>
        <div className="form-row" style={{ marginTop: 0 }}>
          <input
            placeholder="Object path, e.g. Device.WiFi.SSID."
            value={objectPath}
            onChange={(e) => setObjectPath(e.target.value)}
            disabled={busy || !writable}
          />
          <button className="btn" onClick={handleAddObject} disabled={busy || !writable || !objectPath.trim()}>
            Add
          </button>
          <button className="btn danger" onClick={handleDeleteObject} disabled={busy || !writable || !objectPath.trim()}>
            Delete
          </button>
        </div>
        <p style={{ color: "var(--ink-faint)", fontSize: "0.76rem", marginTop: "0.6rem", marginBottom: 0 }}>
          Add creates a new instance under a parent path (e.g. <code>Device.WiFi.SSID.</code>) — the CPE assigns the instance number, shown once the job succeeds. Delete removes one specific instance (e.g. <code>Device.WiFi.SSID.3.</code>). Both must end in "."
        </p>
      </div>

      <div className="panel">
        <h3>Parameter attributes</h3>
        <div className="form-row" style={{ marginTop: 0 }}>
          <input
            placeholder="Parameter path to set, e.g. Device.DeviceInfo.SoftwareVersion"
            value={attrSetPath}
            onChange={(e) => setAttrSetPath(e.target.value)}
            disabled={busy || !writable}
          />
          <select
            aria-label="Notification level"
            value={attrNotification}
            onChange={(e) => setAttrNotification(Number(e.target.value))}
            disabled={busy || !writable}
            style={{ maxWidth: "9rem" }}
          >
            <option value={0}>0 — off</option>
            <option value={1}>1 — passive</option>
            <option value={2}>2 — active</option>
          </select>
          <button className="btn" onClick={handleSetParameterAttribute} disabled={busy || !writable || !attrSetPath.trim()}>
            Set
          </button>
        </div>
        <div className="form-row">
          <input
            placeholder="Paths to read, comma-separated"
            value={attrGetPaths}
            onChange={(e) => setAttrGetPaths(e.target.value)}
            disabled={busy || !writable}
          />
          <button className="btn" onClick={handleGetParameterAttributes} disabled={busy || !writable || !attrGetPaths.trim()}>
            Get
          </button>
        </div>
        <p style={{ color: "var(--ink-faint)", fontSize: "0.76rem", marginTop: "0.6rem", marginBottom: 0 }}>
          Configures whether the CPE actively Informs on a parameter's change (2), passively reports it on the next Inform anyway (1), or neither (0) — rather than the ACS having to poll for it. Get reads the current notification level back; the result appears inline in Recent jobs once the job succeeds.
        </p>
      </div>

      <DeviceTenancy deviceId={id} customerId={device.customer_id} writable={writable} />
      <DeviceConsole id={id} writable={writable} />
      <RemoteShell id={id} writable={writable} />
      <DeviceWebGUI id={id} writable={writable} />
      <VPNTunnel id={id} writable={writable} />

      <div className="panel">
        <h3>Upload from device</h3>
        <div style={{ display: "flex", gap: "0.5rem", flexWrap: "wrap" }}>
          <button className="btn" onClick={() => handleRequestUpload("1 Vendor Configuration File")} disabled={busy || !writable}>
            Request config backup
          </button>
          <button className="btn" onClick={() => handleRequestUpload("2 Vendor Log File")} disabled={busy || !writable}>
            Request log file
          </button>
        </div>
        <p style={{ color: "var(--ink-faint)", fontSize: "0.76rem", marginTop: "0.6rem", marginBottom: 0 }}>
          The CPE pushes the file back independently of this session — watch Recent Jobs for completion, then fetch it via <code>GET /api/v1/devices/{"{id}"}/uploads</code>.
        </p>
      </div>
    </div>
  );
}
