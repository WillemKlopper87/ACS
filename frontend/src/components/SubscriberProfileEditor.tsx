import { useEffect, useState } from "react";
import { api, ApiError, type AccountProfile } from "../api/client";
import { toast } from "../lib/toast";

// Edit an account's subscriber profile (subscriber.manage): fill in a name
// the BSS has not sent, and set or clear VIP. A name from the BSS is shown
// read-only; it is changed in the BSS.

export function SubscriberProfileEditor({ accountId, onClose, onSaved }: { accountId: string; onClose: () => void; onSaved: () => void }) {
  const [profile, setProfile] = useState<AccountProfile | null>(null);
  const [name, setName] = useState("");
  const [vip, setVip] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .getAccountProfile(accountId)
      .then((p) => {
        setProfile(p);
        setName(p.display_name ?? "");
        setVip(p.vip);
      })
      .catch((err) => setError(err instanceof ApiError ? err.message : "Could not load the profile"));
  }, [accountId]);

  const fromBSS = profile?.name_source === "bss";

  const save = async () => {
    if (!profile) return;
    const body: { subscriber_name?: string; vip?: boolean } = {};
    if (!fromBSS && name.trim() !== (profile.display_name ?? "")) body.subscriber_name = name.trim();
    if (vip !== profile.vip) body.vip = vip;
    if (Object.keys(body).length === 0) {
      onClose();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.updateAccountProfile(accountId, body);
      toast("Subscriber profile saved", "success");
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not save the profile");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={() => !busy && onClose()}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="subscriber-edit-title" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h3 id="subscriber-edit-title">Subscriber · account {accountId}</h3>
          <button className="close-detail" onClick={onClose} aria-label="Close" disabled={busy}>
            ✕
          </button>
        </div>
        {error && <div className="banner error">{error}</div>}
        {!profile && !error ? (
          <p className="dim">Loading…</p>
        ) : (
          profile && (
            <div className="modal-body wifi-form">
              <div className="sim-field">
                <label htmlFor="subscriber-name">Subscriber name</label>
                <input id="subscriber-name" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} disabled={fromBSS} autoFocus={!fromBSS} />
                <span className="dim" style={{ fontSize: "0.78rem" }}>
                  {fromBSS ? "Sent by the BSS; change it there." : "Shown until the BSS sends one, which replaces it. Personal information: lookups by name are audited."}
                </span>
              </div>
              <label className="wifi-check">
                <input type="checkbox" checked={vip} onChange={(e) => setVip(e.target.checked)} /> On the VIP list
              </label>
            </div>
          )
        )}
        <div className="modal-foot">
          <button className="btn primary" onClick={save} disabled={busy || !profile}>
            Save
          </button>
          <button className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
