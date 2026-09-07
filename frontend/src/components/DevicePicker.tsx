import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Device } from "../api/types";
import { StatusBadge } from "./StatusBadge";
import { useEscape } from "../lib/hotkeys";

// Adding devices to a group used to mean pasting raw UUIDs into a text box
// — identifiers that appear nowhere an operator normally works. This is the
// replacement: search the fleet the way the Device Fleet screen does, tick
// the devices you want, confirm.
//
// Devices are fetched once per open and filtered in the browser, matching
// DeviceFleet's approach (GET /api/v1/devices has no server-side search;
// page_size is capped at 500 by the API).
const PAGE_SIZE = 500;

export function DevicePicker({
  title,
  confirmLabel,
  excludeIds = [],
  customerId,
  customerLabel,
  onConfirm,
  onClose,
}: {
  title: string;
  confirmLabel: string;
  excludeIds?: string[];
  /**
   * When the thing being filled belongs to a customer, only that customer's
   * devices may be added — the API enforces it and returns 400 otherwise.
   * Filtering here means an operator never picks devices that are going to
   * be refused, rather than discovering it after selecting fifty of them.
   */
  customerId?: string | null;
  customerLabel?: string;
  onConfirm: (deviceIds: string[]) => Promise<void> | void;
  onClose: () => void;
}) {
  const [devices, setDevices] = useState<Device[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);

  useEscape(onClose, true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await api.listDevices(1, PAGE_SIZE);
        if (!cancelled) setDevices(res.items);
      } catch (e) {
        if (!cancelled) setError(e instanceof ApiError ? e.message : "Failed to load devices");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    searchRef.current?.focus();
  }, [loading]);

  const excluded = useMemo(() => new Set(excludeIds), [excludeIds]);

  const matches = useMemo(() => {
    const q = search.trim().toLowerCase();
    return devices.filter((d) => {
      if (excluded.has(d.id)) return false;
      if (customerId && d.customer_id !== customerId) return false;
      if (!q) return true;
      return (
        d.label?.toLowerCase().includes(q) ||
        d.serial_number?.toLowerCase().includes(q) ||
        d.manufacturer?.toLowerCase().includes(q) ||
        d.product_class?.toLowerCase().includes(q) ||
        d.oui_serial?.toLowerCase().includes(q) ||
        d.location?.toLowerCase().includes(q)
      );
    });
  }, [devices, search, excluded, customerId]);

  const allMatchingPicked = matches.length > 0 && matches.every((d) => picked.has(d.id));

  function toggle(id: string) {
    setPicked((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  // Acts on the current filter, not the whole fleet — "select all" after a
  // search for "Zyxel" should mean the Zyxel devices, not everything.
  function toggleAllMatching() {
    setPicked((prev) => {
      const next = new Set(prev);
      if (allMatchingPicked) matches.forEach((d) => next.delete(d.id));
      else matches.forEach((d) => next.add(d.id));
      return next;
    });
  }

  async function confirm() {
    if (picked.size === 0) return;
    setBusy(true);
    try {
      await onConfirm([...picked]);
      onClose();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Failed to add the selected devices");
      setBusy(false);
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="device-picker-title"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="modal-head">
          <h3 id="device-picker-title">{title}</h3>
          <button className="close-detail" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>

        {error && <div className="banner error">{error}</div>}
        {customerId && (
          <p className="dim" style={{ margin: 0, fontSize: "0.8rem" }}>
            Showing only devices belonging to {customerLabel ?? "this customer"} — a group can only
            contain devices from its own customer.
          </p>
        )}

        <div className="form-row">
          <input
            ref={searchRef}
            aria-label="Search devices by name, serial, vendor, model or site"
            placeholder="Search by name, serial, vendor, model or site…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <button className="btn sm" onClick={toggleAllMatching} disabled={matches.length === 0}>
            {allMatchingPicked ? "Clear" : "Select"} all {matches.length}
          </button>
        </div>

        <div className="modal-body">
          {loading ? (
            <div className="loading">Loading devices…</div>
          ) : matches.length === 0 ? (
            <p className="dim" style={{ margin: 0 }}>
              {devices.length === 0
                ? "No devices have reported to this ACS yet."
                : customerId
                  ? "No devices from this customer match — either they are already members, or no device is assigned to this customer yet (assign one in Tenancy, or on the device's detail panel)."
                  : "No devices match that search — every match may already be a member."}
            </p>
          ) : (
            <ul className="pick-list">
              {matches.map((d) => (
                <li key={d.id}>
                  <label>
                    <input type="checkbox" checked={picked.has(d.id)} onChange={() => toggle(d.id)} />
                    <span className="pick-serial">{d.label || d.serial_number || d.oui_serial}</span>
                    <span className="dim">
                      {d.label ? `${d.serial_number || d.oui_serial} · ` : ""}
                      {d.manufacturer} {d.product_class}
                      {d.location ? ` · ${d.location}` : ""}
                    </span>
                    <StatusBadge value={d.online_status} />
                  </label>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="modal-foot">
          <span className="dim">{picked.size} selected</span>
          <span style={{ flex: 1 }} />
          <button className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={confirm} disabled={picked.size === 0 || busy}>
            {busy ? "Adding…" : `${confirmLabel} (${picked.size})`}
          </button>
        </div>
      </div>
    </div>
  );
}
