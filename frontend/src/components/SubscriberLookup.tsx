import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, ApiError, type SubscriberHit } from "../api/client";
import { devicePath } from "../lib/deviceSections";
import { serialTail } from "../lib/subscriber";

// Find a caller's CPE across the whole fleet (GET /api/v1/lookup), not just
// the loaded page: by name, address, account number, the last digits of
// the serial, a device label, or the VIP list. Runs on Enter rather than
// per keystroke, because name and address lookups are audited.

const MATCH_LABEL: Record<string, string> = { serial: "serial", account: "account", name: "name", label: "label", address: "address" };

export function VipBadge() {
  return (
    <span className="pill pill-vip" title="On the VIP list">
      VIP
    </span>
  );
}

function place(h: SubscriberHit) {
  const s = h.site;
  if (!s) return "";
  return [s.street, s.suburb, s.city].filter(Boolean).join(", ");
}

export function SubscriberLookup() {
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const [vipOnly, setVipOnly] = useState(false);
  const [hits, setHits] = useState<SubscriberHit[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = async (query = q, vip = vipOnly) => {
    if (!query.trim() && !vip) {
      setHits(null);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      setHits((await api.lookupSubscribers(query.trim(), vip)).items);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Lookup failed");
    } finally {
      setBusy(false);
    }
  };

  const clear = () => {
    setQ("");
    setVipOnly(false);
    setHits(null);
    setError(null);
  };

  return (
    <div className="panel subscriber-lookup">
      <form
        className="form-row"
        onSubmit={(e) => {
          e.preventDefault();
          void run();
        }}
      >
        <input
          aria-label="Find a subscriber"
          placeholder="Find a subscriber: name, address, account number, last serial digits…"
          value={q}
          maxLength={100}
          onChange={(e) => setQ(e.target.value)}
          style={{ flex: 1, minWidth: "14rem" }}
        />
        <label className="wifi-check" style={{ margin: 0 }}>
          <input
            type="checkbox"
            checked={vipOnly}
            onChange={(e) => {
              setVipOnly(e.target.checked);
              void run(q, e.target.checked);
            }}
          />{" "}
          VIP only
        </label>
        <button className="btn primary" type="submit" disabled={busy || (!q.trim() && !vipOnly)}>
          {busy ? "Searching…" : "Find"}
        </button>
        {hits !== null && (
          <button className="btn" type="button" onClick={clear}>
            Clear
          </button>
        )}
      </form>
      {error && <div className="banner error">{error}</div>}
      {hits !== null && !error && (
        <div className="lookup-results" role="list" aria-label="Lookup results">
          {hits.length === 0 ? (
            <p className="dim" style={{ margin: "0.5rem 0 0" }}>
              No subscriber matches. Try fewer words, the account number, or the last 4 digits of the serial.
            </p>
          ) : (
            <>
              <p className="dim" style={{ margin: "0.5rem 0 0.25rem", fontSize: "0.78rem" }}>
                {hits.length} match{hits.length === 1 ? "" : "es"}
                {hits.length >= 50 ? " (the first 50; add a word to narrow it)" : ""}. Several people can share a name: check the address, account
                or serial digits before acting.
              </p>
              {hits.map((h) => (
                <button key={h.device_id} role="listitem" className="lookup-hit" onClick={() => navigate(devicePath(h.device_id))}>
                  <span className="lookup-hit-main">
                    {h.vip && <VipBadge />} <strong>{h.subscriber_name || h.label || h.oui_serial}</strong>
                    {h.account_id && <span className="mono dim"> · {h.account_id}</span>}
                  </span>
                  <span className="dim lookup-hit-sub">
                    {[place(h), [h.manufacturer, h.model].filter(Boolean).join(" "), serialTail(h.oui_serial)].filter(Boolean).join(" · ")}
                  </span>
                  <span className="lookup-hit-match">
                    {h.matched.map((m) => (
                      <span key={m} className="pill pill-neutral">
                        {MATCH_LABEL[m] ?? m}
                      </span>
                    ))}
                  </span>
                </button>
              ))}
            </>
          )}
        </div>
      )}
    </div>
  );
}
