import { useEffect, useState } from "react";
import { api, ApiError, type AccountProfile } from "../api/client";
import type { Device, SubscriberSummary } from "../api/types";
import { maskName } from "../lib/subscriber";
import { toast } from "../lib/toast";
import { VipBadge } from "./SubscriberLookup";
import { SubscriberProfileEditor } from "./SubscriberProfileEditor";

// Who this device serves, on the Ownership page: account, subscriber name,
// VIP and area. The name is masked and the street is not sent with the
// device at all; "Show details" fetches them through an audited call
// (SubscriberDetailsRevealed). Editing follows the profile rules: a name
// from the BSS is read-only, VIP needs subscriber.manage.

function place(site?: SubscriberSummary["site"], full = false) {
  if (!site) return "";
  const parts = full ? [site.name, site.street, site.suburb, site.city, site.postal_code] : [site.suburb, site.city];
  return parts.filter(Boolean).join(", ");
}

export function DeviceSubscriberPanel({ device, canEdit, onChanged }: { device: Device; canEdit: boolean; onChanged: () => void }) {
  const sub = device.subscriber;
  const accountId = sub?.account_id;
  const [profile, setProfile] = useState<AccountProfile | null>(null);
  const [revealed, setRevealed] = useState<SubscriberSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState(false);

  useEffect(() => {
    setRevealed(null);
    setProfile(null);
    if (!accountId) return;
    api.getAccountProfile(accountId).then(setProfile).catch(() => setProfile(null));
  }, [accountId, device.id, sub?.subscriber_name, sub?.vip]);

  if (!sub || !accountId) {
    return (
      <div className="panel">
        <h3>Subscriber</h3>
        <p className="dim" style={{ margin: 0, fontSize: "0.84rem" }}>
          Not linked to a BSS account. The BSS links devices to accounts; admins can also link one under BSS / NaaS.
        </p>
      </div>
    );
  }

  const reveal = async () => {
    setBusy(true);
    try {
      setRevealed(await api.revealDeviceSubscriber(device.id));
    } catch (err) {
      toast(err instanceof ApiError ? err.message : "Could not show the details", "error");
    } finally {
      setBusy(false);
    }
  };

  const name = revealed?.subscriber_name ?? sub.subscriber_name;
  const source = profile?.name_source === "bss" ? "from the BSS" : profile?.name_source === "operator" ? "typed by an operator" : "";

  return (
    <div className="panel">
      <h3>
        Subscriber {sub.vip && <VipBadge />}
      </h3>
      <div className="param-row">
        <span className="path">Account</span>
        <span className="mono">{accountId}</span>
      </div>
      <div className="param-row">
        <span className="path">Name</span>
        <span>
          {name ? (revealed ? name : maskName(name)) : <span className="dim">not set</span>}
          {name && source && <span className="dim"> · {source}</span>}
        </span>
      </div>
      <div className="param-row">
        <span className="path">VIP</span>
        <span>{sub.vip ? "Yes" : "No"}</span>
      </div>
      <div className="param-row">
        <span className="path">Address</span>
        <span>{revealed ? place(revealed.site, true) || <span className="dim">none recorded</span> : place(sub.site) ? `${place(sub.site)} (street hidden)` : <span className="dim">hidden</span>}</span>
      </div>
      <div className="form-row" style={{ marginTop: "0.5rem" }}>
        {revealed ? (
          <button className="btn sm" onClick={() => setRevealed(null)}>
            Hide details
          </button>
        ) : (
          <button className="btn sm" onClick={reveal} disabled={busy} title="Shows the full name and address; recorded in the audit log">
            Show details
          </button>
        )}
        {canEdit && (
          <button className="btn sm" onClick={() => setEditing(true)}>
            Edit subscriber
          </button>
        )}
        <span className="dim" style={{ fontSize: "0.76rem" }}>
          Showing details is recorded in the audit log.
        </span>
      </div>
      {editing && (
        <SubscriberProfileEditor
          accountId={accountId}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            setRevealed(null);
            onChanged();
          }}
        />
      )}
    </div>
  );
}
