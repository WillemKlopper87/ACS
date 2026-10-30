// XMPP Connection Request (TR-069 Annex K) for CPEs behind carrier CGNAT,
// where HTTP Connection Requests can never reach them (field test
// 2026-10-30, Nokia FastMile). Shows the ACS's XMPP session, what the CPE
// reports, and the zero-touch provisioning run (internal/xmppprov), and
// starts or re-runs it. Each step needs one CWMP session, so a run takes
// several periodic check-ins. Runbook: docs/XMPP-CONNECTION-REQUEST.md.
import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { DeviceXMPPState, XMPPProvisionRequest, XMPPProvisioning } from "../api/client";
import { timeAgo } from "../lib/format";

const POLL_MS = 15_000;

const STEPS: { state: XMPPProvisioning["state"]; label: string }[] = [
  { state: "ADD_CONNECTION", label: "Create the XMPP connection" },
  { state: "DISCOVER_CONNECTION", label: "Find the connection instance" },
  { state: "ADD_SERVER", label: "Add the server entry" },
  { state: "DISCOVER_SERVER", label: "Find the server instance" },
  { state: "CONFIGURE", label: "Write server, account and keep-alive" },
  { state: "BIND", label: "Point Connection Requests at XMPP" },
  { state: "VERIFY", label: "Wait for the device to log in" },
];

const terminal = (s?: string) => s === "DONE" || s === "FAILED";
const supportsXMPP = (methods?: string) => (methods ?? "").split(",").some((m) => m.trim().toUpperCase() === "XMPP");

function ProvisionDialog({ restart, onCancel, onStart }: {
  restart: boolean;
  onCancel: () => void;
  onStart: (body: XMPPProvisionRequest) => Promise<void>;
}) {
  const [relax, setRelax] = useState(false);
  const [bypass, setBypass] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Capture phase, stopped here: DeviceDetail closes itself on Escape too.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      if (!busy) onCancel();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [busy, onCancel]);

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      await onStart({ relax_certificate_verification: relax, bypass_hostname_check: bypass, restart });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={() => !busy && onCancel()}>
      <div className="modal xmpp-setup" role="dialog" aria-modal="true" aria-labelledby="xmpp-setup-title" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h3 id="xmpp-setup-title">Set up XMPP Connection Request</h3>
          <button className="close-detail" onClick={onCancel} aria-label="Close" disabled={busy}>
            ✕
          </button>
        </div>
        <p className="dim" style={{ margin: 0, fontSize: "0.82rem" }}>
          The ACS creates an XMPP account for this device and configures the device to log in to the ACS's XMPP server. Each
          step runs on one check-in, so expect several minutes.{restart ? " This replaces the device's current XMPP account." : ""}
        </p>
        <div className="banner info" style={{ fontSize: "0.8rem" }}>
          The XMPP password is sent to the device in a parameter write. Until the ACS serves CWMP over HTTPS, it crosses the
          network unencrypted.
        </div>
        {error && <div className="banner error">{error}</div>}
        <div className="xmpp-opt">
          <input id="xmpp-relax" type="checkbox" checked={relax} onChange={(e) => setRelax(e.target.checked)} />
          <label htmlFor="xmpp-relax">
            Don't verify the server certificate
            <span className="dim">Needed for the lab's self-signed XMPP certificate, which the device can't be given a CA for.</span>
          </label>
        </div>
        <div className="xmpp-opt">
          <input id="xmpp-bypass" type="checkbox" checked={bypass} onChange={(e) => setBypass(e.target.checked)} />
          <label htmlFor="xmpp-bypass">
            Skip the hostname check
            <span className="dim">Needed when the device dials an IP address but the certificate names the XMPP domain.</span>
          </label>
        </div>
        <p className="dim" style={{ margin: 0, fontSize: "0.76rem" }}>
          Leave both off when the XMPP server has a CA-issued certificate for the name the device dials.
        </p>
        <div className="modal-foot">
          <button className="btn primary" onClick={start} disabled={busy}>
            Start provisioning
          </button>
          <button className="btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}

export function XmppPanel({ deviceId, writable }: { deviceId: string; writable: boolean }) {
  const [state, setState] = useState<DeviceXMPPState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dialog, setDialog] = useState(false);

  const load = useCallback(
    () =>
      api.getDeviceXMPP(deviceId).then(
        (s) => {
          setState(s);
          setError(null);
        },
        (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
      ),
    [deviceId],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const run = state?.provisioning ?? null;
  const running = run !== null && !terminal(run.state);
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(timer);
  }, [running, load]);

  if (error && !state) {
    return (
      <div className="panel">
        <h3>XMPP Connection Request</h3>
        <div className="banner error">Could not load XMPP state: {error}</div>
      </div>
    );
  }
  if (!state) return null;
  const acs = state.acs ?? {};
  const cpe = state.cpe ?? {};
  if (!run && !supportsXMPP(cpe.supported_conn_req_methods)) return null;

  // VERIFY records what the CPE reported on the provisioning run too.
  const connectionStatus = cpe.xmpp_connection_status || run?.connection_status || "";
  const done = run?.state === "DONE";
  const failed = run?.state === "FAILED";
  const current = STEPS.findIndex((s) => s.state === run?.state);
  const blocked = !acs.configured
    ? "XMPP is not enabled on this ACS. Set ACS_XMPP_ENABLED=1 and restart (docs/XMPP-CONNECTION-REQUEST.md)."
    : !acs.connected
      ? `The ACS is not connected to its XMPP server${acs.last_error ? `: ${acs.last_error}` : "."}`
      : "";
  const action = running ? null : done ? "Re-provision…" : failed ? "Retry…" : "Set up…";

  return (
    <div className="panel">
      <h3>
        XMPP Connection Request
        {action && (
          <button className="btn sm" onClick={() => setDialog(true)} disabled={!writable || blocked !== ""}>
            {action}
          </button>
        )}
      </h3>

      {blocked && <p className="dim xmpp-note">{blocked}</p>}

      <div className="param-row">
        <span className="path" style={{ fontFamily: "var(--font-ui)" }}>
          ACS XMPP server
        </span>
        <span className="val">{!acs.configured ? "not enabled" : acs.connected ? "connected" : "disconnected"}</span>
        {acs.configured && (
          <span className="src">
            {acs.jid || "—"}
            {acs.connected_since ? ` · since ${timeAgo(acs.connected_since)}` : ""} · devices dial {acs.cpe_server_address || "?"}:{acs.cpe_server_port ?? 5222}
          </span>
        )}
      </div>
      <div className="param-row">
        <span className="path" style={{ fontFamily: "var(--font-ui)" }}>
          Device Jabber ID
        </span>
        <span className="val">{cpe.conn_req_jabber_id || "not set"}</span>
        <span className="src">
          Supports {cpe.supported_conn_req_methods || "unknown methods"}
          {cpe.conn_req_jabber_id && !cpe.conn_req_jabber_id_valid ? " · not a valid full JID" : ""}
        </span>
      </div>
      {connectionStatus && (
        <div className="param-row">
          <span className="path" style={{ fontFamily: "var(--font-ui)" }}>
            Device XMPP connection
          </span>
          <span className="val">{connectionStatus}</span>
        </div>
      )}

      {done && (
        <p className="dim xmpp-note">
          Connection Requests go over XMPP{run?.completed_at ? `, set up ${timeAgo(run.completed_at)}` : ""}
          {run?.requested_by ? ` by ${run.requested_by}` : ""}.
        </p>
      )}
      {failed && run?.last_error && <div className="banner error xmpp-note">Provisioning failed: {run.last_error}</div>}

      {running && run && (
        <>
          <p className="dim xmpp-note">
            Provisioning since {timeAgo(run.created_at)}
            {run.requested_by ? ` (started by ${run.requested_by})` : ""} · attempt {run.attempts} · each step waits for one check-in.
          </p>
          <ol className="xmpp-steps" aria-label="Provisioning steps">
            {STEPS.map((s, i) => (
              <li key={s.state} aria-current={i === current ? "step" : undefined} className={i < current ? "done" : i === current ? "now" : ""}>
                {s.label}
              </li>
            ))}
          </ol>
          {run.last_error && <p className="dim xmpp-note">Last error: {run.last_error}</p>}
        </>
      )}

      {dialog && (
        <ProvisionDialog
          restart={done || failed}
          onCancel={() => setDialog(false)}
          onStart={async (body) => {
            const res = await api.provisionXMPP(deviceId, body);
            setDialog(false);
            if (res.provisioning) setState((s) => (s ? { ...s, provisioning: res.provisioning } : s));
            void load();
          }}
        />
      )}
    </div>
  );
}
