// SIM / subscriber identity panel. Identifiers (IMSI, ICCID, MSISDN,
// IMEI, EID) are personal information (POPIA) and RICA-registered
// identity: the API returns them masked to every role, and only a human
// superadmin can reveal full values, with a stated reason that the server
// records in the audit log (cmd/api/sim_handlers.go). Revealed values live
// only in this component's memory and are hidden again after two minutes.
import { useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { SIMView } from "../api/client";
import { timeAgo } from "../lib/format";

const REVEAL_TTL_MS = 120_000;
const MIN_REASON = 10;

const KIND_LABEL: Record<string, string> = { imsi: "IMSI", iccid: "ICCID", msisdn: "MSISDN", imei: "IMEI", eid: "EID (eSIM)" };

function contextLabel(path: string): string {
  const leaf = path.slice(path.lastIndexOf(".") + 1);
  if (leaf === "Status") return path.includes("ESIM") ? "eSIM status" : "SIM status";
  return leaf.replace(/^X_[A-Z0-9-]+_/, "");
}

type Field = SIMView["identifiers"][number];

function FieldRow({ label, field }: { label: string; field: Field }) {
  return (
    <div className="param-row">
      <span className="path" style={{ fontFamily: "var(--font-ui)" }}>
        {label}
      </span>
      <span className="val">{field.value || "—"}</span>
      <span className="src">
        {field.path} · read {timeAgo(field.updated_at)}
      </span>
    </div>
  );
}

function RevealDialog({ onCancel, onReveal }: { onCancel: () => void; onReveal: (reason: string, reference: string) => Promise<void> }) {
  const [reason, setReason] = useState("");
  const [reference, setReference] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Capture phase, stopped here: DeviceDetail closes itself on Escape too,
  // and Escape in this dialog must only close the dialog.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      if (!busy) onCancel();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [busy, onCancel]);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await onReveal(reason.trim(), reference.trim());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={() => !busy && onCancel()}>
      <div className="modal sim-reveal" role="dialog" aria-modal="true" aria-labelledby="sim-reveal-title" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h3 id="sim-reveal-title">Reveal subscriber identifiers</h3>
          <button className="close-detail" onClick={onCancel} aria-label="Close" disabled={busy}>
            ✕
          </button>
        </div>
        <p className="dim" style={{ margin: 0, fontSize: "0.82rem" }}>
          Full IMSI, ICCID, MSISDN, IMEI and EID values are personal information (POPIA) and RICA-registered identity. This
          disclosure is recorded in the audit log with your name, the fields shown and your reason. The values are hidden
          again after two minutes.
        </p>
        {error && <div className="banner error">{error}</div>}
        <div className="sim-field">
          <label htmlFor="sim-reveal-reason">Reason (required, at least {MIN_REASON} characters)</label>
          <textarea id="sim-reveal-reason" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} autoFocus placeholder="e.g. RICA subscriber verification for ticket 4471" />
        </div>
        <div className="sim-field">
          <label htmlFor="sim-reveal-reference">Reference (optional: case, ticket or directive number)</label>
          <input id="sim-reveal-reference" value={reference} onChange={(e) => setReference(e.target.value)} />
        </div>
        <div className="modal-foot">
          <button className="btn primary" onClick={submit} disabled={busy || reason.trim().length < MIN_REASON}>
            Reveal and record
          </button>
          <button className="btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}

export function SimPanel({ deviceId, role, writable }: { deviceId: string; role: string | null; writable: boolean }) {
  const [view, setView] = useState<SIMView | null>(null);
  const [revealed, setRevealed] = useState<SIMView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dialog, setDialog] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const [reading, setReading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setRevealed(null);
    api.getSIM(deviceId).then(
      (v) => !cancelled && setView(v),
      (e: unknown) => !cancelled && setError(e instanceof Error ? e.message : String(e)),
    );
    return () => {
      cancelled = true;
    };
  }, [deviceId]);

  useEffect(() => {
    if (!revealed) return;
    const timer = setTimeout(() => setRevealed(null), REVEAL_TTL_MS);
    return () => clearTimeout(timer);
  }, [revealed]);

  if (error) {
    return (
      <div className="panel">
        <h3>SIM</h3>
        <div className="banner error">Could not load SIM information: {error}</div>
      </div>
    );
  }
  if (!view || !view.supported) return null;

  // The backend refuses anyone but a human superadmin; lab mode without
  // auth (role null) is refused too, so the button is not offered there.
  const canReveal = role === "superadmin";
  const shown = revealed ?? view;
  const refreshPaths = view.refresh_paths ?? [];

  const readValues = async () => {
    setReading(true);
    try {
      const res = await api.getParametersLive(deviceId, refreshPaths);
      setNote(`Queued ${res.command_key}. The values appear after the device's next check-in.`);
    } catch (e) {
      setNote(e instanceof ApiError || e instanceof Error ? e.message : String(e));
    } finally {
      setReading(false);
    }
  };

  return (
    <div className="panel">
      <h3>
        SIM
        {canReveal &&
          (revealed ? (
            <button className="btn sm" onClick={() => setRevealed(null)}>
              Hide
            </button>
          ) : (
            shown.identifiers.length > 0 && (
              <button className="btn sm" onClick={() => setDialog(true)}>
                Reveal full values…
              </button>
            )
          ))}
      </h3>

      {revealed && (
        <div className="banner info sim-revealed">Full values shown and recorded in the audit log. They hide again within two minutes.</div>
      )}

      {shown.identifiers.map((f) => (
        <FieldRow key={f.path} label={KIND_LABEL[f.kind] ?? f.kind.toUpperCase()} field={f} />
      ))}
      {shown.context.map((f) => (
        <FieldRow key={f.path} label={contextLabel(f.path)} field={f} />
      ))}

      {!revealed && shown.identifiers.length > 0 && (
        <p className="dim" style={{ margin: "0.5rem 0 0", fontSize: "0.75rem" }}>
          Masked for every role.{canReveal ? "" : " Only a superadmin can reveal full values, with a recorded reason."}
        </p>
      )}

      {refreshPaths.length > 0 && (
        <div style={{ marginTop: "0.6rem" }}>
          <p className="dim" style={{ margin: "0 0 0.4rem", fontSize: "0.78rem" }}>
            {refreshPaths.length} SIM value{refreshPaths.length === 1 ? " has" : "s have"} not been read from the device yet.
          </p>
          <button className="btn sm" onClick={readValues} disabled={!writable || reading}>
            Read SIM values
          </button>
        </div>
      )}
      {note && (
        <p className="dim" style={{ margin: "0.4rem 0 0", fontSize: "0.78rem" }}>
          {note}
        </p>
      )}

      {dialog && (
        <RevealDialog
          onCancel={() => setDialog(false)}
          onReveal={async (reason, reference) => {
            const v = await api.revealSIM(deviceId, reason, reference);
            setRevealed(v);
            setDialog(false);
          }}
        />
      )}
    </div>
  );
}
