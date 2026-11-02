import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import { toast } from "../lib/toast";
import { editBody, formFor, impactOf, isOpen, pairedNetworks, spareNetworks, type WiFiForm, type WiFiNetwork } from "../lib/wifiEdit";

// Per-network Wi-Fi control for one CPE: every SSID with its band, role,
// security, broadcast and connected clients; edit one at a time (name,
// passphrase and security change together) or turn on a spare network.
// Replaces the old "Set SSID" box, which renamed every primary network at
// once and kicked everyone off without warning.

type View = Awaited<ReturnType<typeof api.getWiFiNetworks>>;

const ROLE_LABEL: Record<string, string> = { primary: "primary", guest: "guest", disabled: "spare", unbound: "mesh / unused" };

function bandLabel(n: WiFiNetwork) {
  return n.band || "unknown band";
}

export function WiFiNetworksPanel({ deviceId, writable }: { deviceId: string; writable: boolean }) {
  const [view, setView] = useState<View | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ network: WiFiNetwork; adding: boolean } | null>(null);
  const [choosingSpare, setChoosingSpare] = useState(false);

  const load = useCallback(async () => {
    try {
      setView(await api.getWiFiNetworks(deviceId));
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not load the Wi-Fi networks");
    }
  }, [deviceId]);

  useEffect(() => {
    void load();
  }, [load]);

  const refresh = async () => {
    if (!view?.refresh_paths?.length) return;
    try {
      const res = await api.getParametersLive(deviceId, view.refresh_paths);
      toast(`Queued ${res.command_key} to read the Wi-Fi settings; they update after the device's next check-in`, "info");
    } catch (err) {
      toast(err instanceof ApiError ? err.message : "Could not queue the read", "error");
    }
  };

  if (error) return <div className="banner error">{error}</div>;
  if (!view) return <p className="dim">Loading Wi-Fi networks…</p>;
  if (!view.supported) return <p className="dim">{view.reason || "No Wi-Fi networks resolved for this device."}</p>;

  const networks = (view.networks ?? []).filter((n) => n.access_point || n.role === "unbound");
  const clients = view.clients_by_instance ?? null;
  const spares = spareNetworks(view.networks ?? []);

  return (
    <>
      {!view.complete && (
        <div className="banner warn">
          Some Wi-Fi settings are not read yet, so editing is held.{" "}
          <button className="btn sm" onClick={refresh}>
            Read them now
          </button>
        </div>
      )}
      <div className="table-wrap">
      <table aria-label="Wi-Fi networks">
        <thead>
          <tr>
            <th>Network</th>
            <th>Band</th>
            <th>Role</th>
            <th>State</th>
            <th>Security</th>
            <th>Broadcast</th>
            <th>Clients</th>
            <th aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {networks.map((n) => (
            <tr key={n.instance}>
              <td>
                <strong>{n.name || "(no name)"}</strong> <span className="dim">· SSID {n.instance}</span>
              </td>
              <td>{bandLabel(n)}</td>
              <td>
                <span className={`pill ${n.role === "primary" ? "pill-ok" : "pill-neutral"}`}>{ROLE_LABEL[n.role] ?? n.role}</span>
              </td>
              <td>{n.enabled === false ? "off" : "on"}</td>
              <td>{n.security_mode || "—"}</td>
              <td>{n.advertised === false ? "hidden" : "yes"}</td>
              <td>{clients ? (clients[String(n.instance)] ?? 0) : "—"}</td>
              <td>
                {n.access_point && n.role !== "disabled" && (
                  <button className="btn sm" disabled={!writable || !view.complete} onClick={() => setEditing({ network: n, adding: false })}>
                    Edit
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      </div>
      <div className="form-row" style={{ marginTop: "0.5rem" }}>
        <button className="btn" disabled={!writable || !view.complete || spares.length === 0} onClick={() => setChoosingSpare(true)}>
          Add a network
        </button>
        <span className="dim" style={{ fontSize: "0.8rem" }}>
          {spares.length === 0 ? "No spare network slot on this device." : `${spares.length} spare slot${spares.length === 1 ? "" : "s"} on this device.`}
          {view.clients_sampled_at ? ` Clients as of ${new Date(view.clients_sampled_at).toLocaleTimeString()}.` : " No recent client sample."}
        </span>
      </div>

      {choosingSpare && (
        <div className="modal-backdrop" onClick={() => setChoosingSpare(false)}>
          <div className="modal" role="dialog" aria-modal="true" aria-labelledby="wifi-spare-title" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <h3 id="wifi-spare-title">Add a network</h3>
              <button className="close-detail" onClick={() => setChoosingSpare(false)} aria-label="Close">
                ✕
              </button>
            </div>
            <p className="dim" style={{ margin: 0, fontSize: "0.82rem" }}>
              This device has fixed network slots. Pick the band; the slot is turned on with the name, security and passphrase you set.
            </p>
            <div className="modal-body">
              {spares.map((n) => (
                <button
                  key={n.instance}
                  className="btn"
                  style={{ display: "block", width: "100%", textAlign: "left", marginBottom: "0.4rem" }}
                  onClick={() => {
                    setChoosingSpare(false);
                    setEditing({ network: n, adding: true });
                  }}
                >
                  {bandLabel(n)} · SSID {n.instance} <span className="dim">(now "{n.name || "unnamed"}", off)</span>
                </button>
              ))}
            </div>
          </div>
        </div>
      )}

      {editing && (
        <WiFiNetworkEditor
          deviceId={deviceId}
          network={editing.network}
          adding={editing.adding}
          all={view.networks ?? []}
          clientsByInstance={clients}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void load();
          }}
        />
      )}
    </>
  );
}

function WiFiNetworkEditor({
  deviceId,
  network,
  adding,
  all,
  clientsByInstance,
  onClose,
  onSaved,
}: {
  deviceId: string;
  network: WiFiNetwork;
  adding: boolean;
  all: WiFiNetwork[];
  clientsByInstance: Record<string, number> | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [form, setForm] = useState<WiFiForm>(() => {
    const f = formFor(network);
    if (adding) return { ...f, name: "", enabled: true, securityMode: network.modes_supported?.includes("WPA2-Personal") ? "WPA2-Personal" : f.securityMode };
    return f;
  });
  const [showPass, setShowPass] = useState(false);
  const [confirmText, setConfirmText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = (patch: Partial<WiFiForm>) => setForm((f) => ({ ...f, ...patch }));
  const pairs = adding ? [] : pairedNetworks(network, all);
  const impact = impactOf(network, form, clientsByInstance);
  const modes = network.modes_supported?.length ? network.modes_supported : network.security_mode ? [network.security_mode] : [];
  // Disrupting a primary network takes its name typed out.
  const needsTyped = impact.disconnects && network.role === "primary";
  const confirmed = !needsTyped || confirmText.trim() === (network.name ?? "");

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await api.updateWiFiNetwork(deviceId, network.instance, editBody(network, form));
      toast(
        `Queued ${res.command_key}${res.disconnects ? " — clients will reconnect when the device applies it" : ""}; applies on the device's next session`,
        "success",
      );
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not queue the change");
    } finally {
      setBusy(false);
    }
  };

  const title = adding ? `Add a network on ${bandLabel(network)}` : `Edit ${network.name || "network"} (SSID ${network.instance}, ${bandLabel(network)})`;
  return (
    <div className="modal-backdrop" onClick={() => !busy && onClose()}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="wifi-edit-title" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h3 id="wifi-edit-title">{title}</h3>
          <button className="close-detail" onClick={onClose} aria-label="Close" disabled={busy}>
            ✕
          </button>
        </div>
        {error && <div className="banner error">{error}</div>}
        <div className="modal-body wifi-form">
          <div className="sim-field">
            <label htmlFor="wifi-name">Network name (SSID)</label>
            <input id="wifi-name" value={form.name} maxLength={32} onChange={(e) => set({ name: e.target.value })} disabled={!network.name_writable} autoFocus />
          </div>
          <div className="sim-field">
            <label htmlFor="wifi-mode">Security</label>
            <select id="wifi-mode" value={form.securityMode} onChange={(e) => set({ securityMode: e.target.value, allowOpen: false })} disabled={!network.security_mode_writable}>
              {modes.map((m) => (
                <option key={m} value={m}>
                  {m === "None" ? "None (open network)" : m}
                </option>
              ))}
            </select>
          </div>
          {!isOpen(form.securityMode) && (
            <div className="sim-field">
              <label htmlFor="wifi-pass">{adding ? "Passphrase (required)" : "New passphrase (leave empty to keep the current one)"}</label>
              <div className="form-row">
                <input
                  id="wifi-pass"
                  type={showPass ? "text" : "password"}
                  autoComplete="new-password"
                  value={form.passphrase}
                  onChange={(e) => set({ passphrase: e.target.value })}
                  disabled={!network.passphrase_writable}
                />
                <button type="button" className="btn sm" onClick={() => setShowPass((s) => !s)}>
                  {showPass ? "Hide" : "Show"}
                </button>
              </div>
            </div>
          )}
          {isOpen(form.securityMode) && form.enabled && (
            <label className="wifi-check">
              <input type="checkbox" checked={form.allowOpen} onChange={(e) => set({ allowOpen: e.target.checked })} /> Anyone nearby may join this open network
            </label>
          )}
          <label className="wifi-check">
            <input type="checkbox" checked={form.advertised} onChange={(e) => set({ advertised: e.target.checked })} disabled={!network.advertised_writable} /> Broadcast the network name
          </label>
          {!adding && (
            <label className="wifi-check">
              <input type="checkbox" checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} disabled={!network.enable_writable} /> Network on
            </label>
          )}
          {pairs.map((p) => (
            <label className="wifi-check" key={p.instance}>
              <input
                type="checkbox"
                checked={form.alsoInstances.includes(p.instance)}
                onChange={(e) =>
                  set({ alsoInstances: e.target.checked ? [...form.alsoInstances, p.instance] : form.alsoInstances.filter((i) => i !== p.instance) })
                }
              />{" "}
              Also apply to SSID {p.instance} ({bandLabel(p)}), the same network on the other band
            </label>
          ))}
        </div>

        <div className={`banner ${impact.disconnects ? "warn" : "info"}`}>{impact.summary}</div>
        {impact.problems.length > 0 && (
          <ul className="dim" style={{ margin: "0.25rem 0", fontSize: "0.82rem" }}>
            {impact.problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        )}
        {needsTyped && impact.problems.length === 0 && (
          <div className="sim-field">
            <label htmlFor="wifi-confirm">
              Type the current network name <strong>{network.name}</strong> to confirm
            </label>
            <input id="wifi-confirm" value={confirmText} onChange={(e) => setConfirmText(e.target.value)} />
          </div>
        )}
        <div className="modal-foot">
          <button className="btn primary" onClick={submit} disabled={busy || impact.problems.length > 0 || !confirmed}>
            {adding ? "Turn on network" : "Apply change"}
          </button>
          <button className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
