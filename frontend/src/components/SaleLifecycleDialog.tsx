import { useState } from "react";
import { api, ApiError, type Onboarding, type SaleEndReason, type ServicePlan, type Site } from "../api/client";
import { toast } from "../lib/toast";

// Changes to a sale in service (internal/lifecycle): end the service,
// swap the device, or move it to another site or plan. Each says what
// will happen before it is sent; ending asks for the serial typed out.

export type SaleLifecycleMode = "end" | "swap" | "site" | "plan";

const END_REASONS: { value: SaleEndReason; label: string }[] = [
  { value: "return", label: "Customer cancelled and returned the device" },
  { value: "rma", label: "Device faulty, sent for repair (RMA)" },
  { value: "upgrade", label: "Replaced by a newer device" },
  { value: "moved", label: "Moved to another account" },
  { value: "corrected", label: "Sold in error" },
];

const TITLES: Record<SaleLifecycleMode, string> = {
  end: "End service",
  swap: "Swap device",
  site: "Change site",
  plan: "Change service plan",
};

export function SaleLifecycleDialog({
  sale,
  mode,
  sites,
  plans,
  onClose,
  onDone,
}: {
  sale: Onboarding;
  mode: SaleLifecycleMode;
  sites: Site[];
  plans: ServicePlan[];
  onClose: () => void;
  onDone: () => void;
}) {
  const [reason, setReason] = useState<SaleEndReason>(mode === "swap" ? "rma" : "return");
  const [archive, setArchive] = useState(true);
  const [reset, setReset] = useState(false);
  const [confirmText, setConfirmText] = useState("");
  const [serial, setSerial] = useState("");
  const [oui, setOui] = useState("");
  // Only the sale's customer's own (or shared) sites and plans.
  const ownSites = sites.filter((s) => !s.customer_id || s.customer_id === sale.customer_id);
  const ownPlans = plans.filter((p) => !p.customer_id || p.customer_id === sale.customer_id);
  const [siteId, setSiteId] = useState(sale.site_id ?? "");
  const [planId, setPlanId] = useState(sale.service_plan_id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const newPlan = ownPlans.find((p) => p.id === planId);
  const planConfigures = !!newPlan && ((newPlan.template_ids?.length ?? 0) > 0 || Object.values(newPlan.cellular ?? {}).some(Boolean));

  const ready = (() => {
    switch (mode) {
      case "end":
        return confirmText.trim() === sale.expected_serial;
      case "swap":
        return serial.trim() !== "" && serial.trim() !== sale.expected_serial;
      case "site":
        return siteId !== "" && siteId !== sale.site_id;
      case "plan":
        return planId !== "" && planId !== sale.service_plan_id;
    }
  })();

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      switch (mode) {
        case "end": {
          const res = await api.endService(sale.id, { reason, keep_subscriber: !archive, factory_reset: reset });
          const parts = [`Service ended; ${sale.expected_serial} is back in the unassigned pool`];
          if (res.subscribers_archived.length) parts.push("subscriber details archived");
          if (res.factory_reset) parts.push(`factory reset ${res.factory_reset}`);
          toast(parts.join("; "), "success");
          break;
        }
        case "swap": {
          const res = await api.swapDevice(sale.id, { reason: reason as "rma" | "upgrade", serial: serial.trim(), oui: oui.trim() || undefined });
          toast(`Replacement ${res.replacement.expected_serial} planned; it goes into service when it checks in`, "success");
          break;
        }
        case "site":
          await api.moveSaleSite(sale.id, siteId);
          toast("Site changed", "success");
          break;
        case "plan": {
          const res = await api.changeSalePlan(sale.id, planId);
          toast(res.state === "IN_SERVICE" ? "Plan changed" : "Plan changed; the device is configured on its next check-in", "success");
          break;
        }
      }
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "The change failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={() => !busy && onClose()}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="sale-lifecycle-title" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h3 id="sale-lifecycle-title">
            {TITLES[mode]}: {sale.expected_serial}
          </h3>
          <button className="close-detail" onClick={onClose} aria-label="Close" disabled={busy}>
            ✕
          </button>
        </div>
        {error && <div className="banner error">{error}</div>}
        <div className="modal-body wifi-form">
          {(mode === "end" || mode === "swap") && (
            <div className="sim-field">
              <label htmlFor="sale-reason">Reason</label>
              <select id="sale-reason" value={reason} onChange={(e) => setReason(e.target.value as SaleEndReason)}>
                {END_REASONS.filter((r) => mode === "end" || r.value === "rma" || r.value === "upgrade").map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </select>
            </div>
          )}

          {mode === "end" && (
            <>
              <ul className="dim" style={{ margin: "0.25rem 0", fontSize: "0.82rem", paddingLeft: "1.1rem" }}>
                <li>The sale is marked Service ended, and the device&apos;s account link{sale.account_ref ? ` (${sale.account_ref})` : ""} is removed.</li>
                <li>The device goes back to the unassigned pool: no customer, site, projects, nickname or location.</li>
              </ul>
              <label className="wifi-check">
                <input type="checkbox" checked={archive} onChange={(e) => setArchive(e.target.checked)} /> Archive the subscriber&apos;s name and VIP flag if
                the account has no other device (kept for the archive period, 3 years by default, then deleted)
              </label>
              <label className="wifi-check">
                <input type="checkbox" checked={reset} onChange={(e) => setReset(e.target.checked)} /> Factory-reset the device, and let it re-enrol for 24
                hours
              </label>
              <div className="sim-field">
                <label htmlFor="sale-confirm">
                  Type the serial <strong>{sale.expected_serial}</strong> to confirm
                </label>
                <input id="sale-confirm" value={confirmText} onChange={(e) => setConfirmText(e.target.value)} autoComplete="off" />
              </div>
            </>
          )}

          {mode === "swap" && (
            <>
              <div className="sim-field">
                <label htmlFor="swap-serial">Replacement serial</label>
                <input id="swap-serial" value={serial} onChange={(e) => setSerial(e.target.value)} autoFocus />
              </div>
              <div className="sim-field">
                <label htmlFor="swap-oui">Replacement OUI (optional)</label>
                <input id="swap-oui" value={oui} onChange={(e) => setOui(e.target.value)} />
              </div>
              <p className="dim" style={{ margin: 0, fontSize: "0.82rem" }}>
                The old device&apos;s service ends and it goes back to the unassigned pool. The replacement gets a new sale with the same account, site and
                plan, and goes into service when it first checks in. The subscriber&apos;s details stay.
              </p>
            </>
          )}

          {mode === "site" && (
            <div className="sim-field">
              <label htmlFor="sale-site">Site</label>
              <select id="sale-site" value={siteId} onChange={(e) => setSiteId(e.target.value)}>
                <option value="">Choose a site</option>
                {ownSites.map((s) => (
                  <option key={s.id} value={s.id}>
                    {[s.name, s.suburb, s.city].filter(Boolean).join(", ")}
                  </option>
                ))}
              </select>
              <span className="dim" style={{ fontSize: "0.78rem" }}>
                Add a new site under Setup → Sites.
              </span>
            </div>
          )}

          {mode === "plan" && (
            <>
              <div className="sim-field">
                <label htmlFor="sale-plan">Service plan</label>
                <select id="sale-plan" value={planId} onChange={(e) => setPlanId(e.target.value)}>
                  <option value="">Choose a plan</option>
                  {ownPlans.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.code} · {p.name}
                    </option>
                  ))}
                </select>
              </div>
              {newPlan && planId !== sale.service_plan_id && (
                <div className={`banner ${planConfigures ? "warn" : "info"}`}>
                  {planConfigures
                    ? "This plan writes configuration: the sale shows as configuring until the device's next check-in applies it."
                    : "This plan writes nothing to the device; it takes effect at once."}
                </div>
              )}
            </>
          )}
        </div>
        <div className="modal-foot">
          <button className={`btn ${mode === "end" ? "danger" : "primary"}`} onClick={submit} disabled={busy || !ready}>
            {TITLES[mode]}
          </button>
          <button className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
