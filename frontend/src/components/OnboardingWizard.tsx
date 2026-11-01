// The new-sale wizard (docs/CONSOLE-UX-PROPOSAL-2026-10-30.md §4, Console):
// device, who, where, what, review. With `device` it claims a CPE that
// already connected; otherwise it records a sale before installation.
import { useEffect, useMemo, useState } from "react";
import { api, ApiError, type Onboarding, type ServicePlan, type Site, type SiteInput } from "../api/client";
import type { ConfigTemplate } from "../api/types";
import { useCustomers, customerName } from "../lib/useCustomers";

export interface ClaimTarget {
  id: string;
  serial_number: string;
  oui_serial: string;
  manufacturer?: string;
  product_class?: string;
}

const STEPS = ["Device", "Who", "Where", "What", "Review"] as const;
type SiteMode = "existing" | "new" | "none";

const emptySite: SiteInput = { name: "", street: "", suburb: "", city: "", postal_code: "", country: "ZA" };

export function OnboardingWizard({ device, onClose, onCreated }: {
  device?: ClaimTarget;
  onClose: () => void;
  onCreated: (o: Onboarding) => void;
}) {
  const customers = useCustomers();
  const [sites, setSites] = useState<Site[]>([]);
  const [plans, setPlans] = useState<ServicePlan[]>([]);
  const [templates, setTemplates] = useState<ConfigTemplate[]>([]);
  const [step, setStep] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [serial, setSerial] = useState(device?.serial_number ?? "");
  const [oui, setOui] = useState("");
  const [iccid, setIccid] = useState("");
  const [imsi, setImsi] = useState("");
  const [imei, setImei] = useState("");
  const [role, setRole] = useState("gateway");
  const [customerId, setCustomerId] = useState("");
  const [accountRef, setAccountRef] = useState("");
  const [commercialModel, setCommercialModel] = useState("retail");
  const [externalRef, setExternalRef] = useState("");
  const [siteMode, setSiteMode] = useState<SiteMode>("existing");
  const [siteId, setSiteId] = useState("");
  const [newSite, setNewSite] = useState<SiteInput>(emptySite);
  const [planId, setPlanId] = useState("");

  useEffect(() => {
    api.listSites().then((r) => setSites(r.items), () => {});
    api.listServicePlans().then((r) => setPlans(r.items), () => {});
    api.listTemplates().then((r) => setTemplates(r.items), () => {});
  }, []);

  // Capture phase, stopped here: the device workspace closes on Escape too.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      if (!busy) onClose();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [busy, onClose]);

  // Sites and plans the chosen customer may use: its own, and shared ones
  // (plans only; a site with no customer is an unassigned address).
  const usableSites = useMemo(() => sites.filter((s) => (s.customer_id ?? "") === customerId), [sites, customerId]);
  const usablePlans = useMemo(() => plans.filter((p) => !p.customer_id || p.customer_id === customerId), [plans, customerId]);
  const plan = plans.find((p) => p.id === planId);
  const site = sites.find((s) => s.id === siteId);
  useEffect(() => {
    if (siteId && !usableSites.some((s) => s.id === siteId)) setSiteId("");
    if (planId && !usablePlans.some((p) => p.id === planId)) setPlanId("");
  }, [usableSites, usablePlans, siteId, planId]);

  const stepValid = [
    serial.trim() !== "",
    true,
    siteMode === "existing" ? siteId !== "" : siteMode === "new" ? newSite.name.trim() !== "" : true,
    true,
    true,
  ];

  async function create() {
    setBusy(true);
    setError(null);
    try {
      let site_id: string | null = siteMode === "existing" ? siteId : null;
      if (siteMode === "new") {
        const created = await api.createSite({ ...newSite, customer_id: customerId || null });
        site_id = created.id;
      }
      const o = await api.createOnboarding({
        device_id: device?.id ?? null,
        expected_serial: device ? undefined : serial.trim(),
        expected_oui: device ? undefined : oui.trim() || undefined,
        expected_iccid: iccid.trim() || undefined,
        expected_imsi: imsi.trim() || undefined,
        expected_imei: imei.trim() || undefined,
        role: role as "gateway",
        customer_id: customerId || null,
        account_ref: accountRef.trim() || undefined,
        commercial_model: commercialModel as "retail",
        external_ref: externalRef.trim() || undefined,
        site_id,
        service_plan_id: planId || null,
      });
      onCreated(o);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Failed to record the sale");
      setBusy(false);
    }
  }

  const templateName = (id: string) => templates.find((t) => t.id === id)?.name ?? id.slice(0, 8);
  const setSiteField = (k: keyof SiteInput) => (e: React.ChangeEvent<HTMLInputElement>) => setNewSite({ ...newSite, [k]: e.target.value });

  return (
    <div className="modal-backdrop" onClick={() => !busy && onClose()}>
      <div className="modal ob-wizard" role="dialog" aria-modal="true" aria-labelledby="ob-wizard-title" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h3 id="ob-wizard-title">{device ? "Claim device" : "New sale"}</h3>
          <button className="close-detail" onClick={onClose} aria-label="Close" disabled={busy}>
            ✕
          </button>
        </div>
        <ol className="ob-steps" aria-label="Steps">
          {STEPS.map((label, i) => (
            <li key={label} className={i === step ? "on" : i < step ? "done" : ""} aria-current={i === step ? "step" : undefined}>
              {label}
            </li>
          ))}
        </ol>

        <div className="modal-body">
          {step === 0 && (
            <fieldset className="ob-fieldset">
              <legend>Which unit and SIM were sold</legend>
              {device ? (
                <p className="ob-claimed">
                  <strong>{device.serial_number}</strong> <span className="dim">{device.oui_serial}</span>
                </p>
              ) : (
                <div className="form-row">
                  <label className="field">
                    <span>Serial number</span>
                    <input value={serial} onChange={(e) => setSerial(e.target.value)} required autoFocus />
                  </label>
                  <label className="field">
                    <span>OUI (optional)</span>
                    <input value={oui} onChange={(e) => setOui(e.target.value)} placeholder="e.g. A4FC77" />
                  </label>
                </div>
              )}
              <div className="form-row">
                <label className="field">
                  <span>SIM ICCID (optional)</span>
                  <input value={iccid} onChange={(e) => setIccid(e.target.value)} inputMode="numeric" autoComplete="off" />
                </label>
                <label className="field">
                  <span>SIM IMSI (optional)</span>
                  <input value={imsi} onChange={(e) => setImsi(e.target.value)} inputMode="numeric" autoComplete="off" />
                </label>
              </div>
              <div className="form-row">
                <label className="field">
                  <span>IMEI (optional)</span>
                  <input value={imei} onChange={(e) => setImei(e.target.value)} inputMode="numeric" autoComplete="off" />
                </label>
                <label className="field">
                  <span>Role</span>
                  <select value={role} onChange={(e) => setRole(e.target.value)}>
                    <option value="gateway">Gateway</option>
                    <option value="extender">Extender / mesh</option>
                    <option value="ont">ONT</option>
                    <option value="other">Other</option>
                  </select>
                </label>
              </div>
              <p className="dim ob-hint">
                The ACS holds the onboarding if the CPE reports a different SIM. SIM identifiers are shown masked after this.
              </p>
            </fieldset>
          )}

          {step === 1 && (
            <fieldset className="ob-fieldset">
              <legend>Who it is for</legend>
              <div className="form-row">
                <label className="field">
                  <span>Customer (ISP tenant)</span>
                  <select value={customerId} onChange={(e) => setCustomerId(e.target.value)}>
                    <option value="">No customer</option>
                    {customers.map((c) => (
                      <option key={c.id} value={c.id}>{c.name}</option>
                    ))}
                  </select>
                </label>
                <label className="field">
                  <span>Commercial model</span>
                  <select value={commercialModel} onChange={(e) => setCommercialModel(e.target.value)}>
                    <option value="retail">Retail</option>
                    <option value="wholesale">Wholesale</option>
                    <option value="enterprise">Enterprise</option>
                  </select>
                </label>
              </div>
              <div className="form-row">
                <label className="field">
                  <span>BSS account reference</span>
                  <input value={accountRef} onChange={(e) => setAccountRef(e.target.value)} placeholder="e.g. ACC-77" />
                </label>
                <label className="field">
                  <span>Order reference (optional)</span>
                  <input value={externalRef} onChange={(e) => setExternalRef(e.target.value)} />
                </label>
              </div>
              <p className="dim ob-hint">Store the BSS reference, not the subscriber's name or contact details.</p>
            </fieldset>
          )}

          {step === 2 && (
            <fieldset className="ob-fieldset">
              <legend>Where it is installed</legend>
              <div className="ob-choice" role="radiogroup" aria-label="Site">
                {(["existing", "new", "none"] as SiteMode[]).map((m) => (
                  <label key={m}>
                    <input type="radio" name="site-mode" checked={siteMode === m} onChange={() => setSiteMode(m)} />
                    {m === "existing" ? "Existing site" : m === "new" ? "New site" : "No site yet"}
                  </label>
                ))}
              </div>
              {siteMode === "existing" && (
                <label className="field">
                  <span>Site</span>
                  <select value={siteId} onChange={(e) => setSiteId(e.target.value)}>
                    <option value="">Choose a site…</option>
                    {usableSites.map((s) => (
                      <option key={s.id} value={s.id}>
                        {s.name}
                        {s.city ? `, ${s.city}` : ""}
                      </option>
                    ))}
                  </select>
                  {usableSites.length === 0 && <span className="dim">No sites for {customerName(customers, customerId || null) === "—" ? "this choice" : customerName(customers, customerId)} yet.</span>}
                </label>
              )}
              {siteMode === "new" && (
                <>
                  <div className="form-row">
                    <label className="field">
                      <span>Site name</span>
                      <input value={newSite.name} onChange={setSiteField("name")} placeholder="e.g. 14 Oak Ave" />
                    </label>
                    <label className="field">
                      <span>BSS premise reference (optional)</span>
                      <input value={newSite.external_ref ?? ""} onChange={setSiteField("external_ref")} />
                    </label>
                  </div>
                  <div className="form-row">
                    <label className="field">
                      <span>Street</span>
                      <input value={newSite.street} onChange={setSiteField("street")} />
                    </label>
                    <label className="field">
                      <span>Suburb</span>
                      <input value={newSite.suburb} onChange={setSiteField("suburb")} />
                    </label>
                  </div>
                  <div className="form-row">
                    <label className="field">
                      <span>City</span>
                      <input value={newSite.city} onChange={setSiteField("city")} />
                    </label>
                    <label className="field">
                      <span>Postal code</span>
                      <input value={newSite.postal_code} onChange={setSiteField("postal_code")} />
                    </label>
                    <label className="field">
                      <span>Country</span>
                      <input value={newSite.country} onChange={setSiteField("country")} />
                    </label>
                  </div>
                </>
              )}
            </fieldset>
          )}

          {step === 3 && (
            <fieldset className="ob-fieldset">
              <legend>What it gets</legend>
              <label className="field">
                <span>Service plan</span>
                <select value={planId} onChange={(e) => setPlanId(e.target.value)}>
                  <option value="">No plan (in service once secured)</option>
                  {usablePlans.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.code} — {p.name}
                    </option>
                  ))}
                </select>
              </label>
              {plan && <PlanSummary plan={plan} templateName={templateName} />}
            </fieldset>
          )}

          {step === 4 && (
            <dl className="kv ob-review">
              <dt>Device</dt>
              <dd>{device ? device.oui_serial : `${oui ? `${oui.toUpperCase()} ` : ""}${serial}`}</dd>
              <dt>SIM</dt>
              <dd>{iccid || imsi ? "checked against the sale" : "not checked"}</dd>
              <dt>Role</dt>
              <dd>{role}</dd>
              <dt>Customer</dt>
              <dd>{customerName(customers, customerId || null)}</dd>
              <dt>Account</dt>
              <dd>{accountRef || "—"} ({commercialModel})</dd>
              <dt>Site</dt>
              <dd>{siteMode === "existing" ? site?.name : siteMode === "new" ? `${newSite.name} (new)` : "—"}</dd>
              <dt>Plan</dt>
              <dd>{plan ? `${plan.code} — ${plan.name}` : "—"}</dd>
            </dl>
          )}
          {error && <div className="banner error" style={{ marginTop: "0.6rem" }}>{error}</div>}
        </div>

        <div className="modal-foot">
          {step > 0 && (
            <button className="btn" onClick={() => setStep(step - 1)} disabled={busy}>
              Back
            </button>
          )}
          {step < STEPS.length - 1 ? (
            <button className="btn primary" onClick={() => setStep(step + 1)} disabled={!stepValid[step]}>
              Next
            </button>
          ) : (
            <button className="btn primary" onClick={create} disabled={busy}>
              {busy ? "Saving…" : device ? "Claim device" : "Record sale"}
            </button>
          )}
          <button className="btn ghost" onClick={onClose} disabled={busy}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}

export function PlanSummary({ plan, templateName }: { plan: ServicePlan; templateName: (id: string) => string }) {
  const c = plan.cellular ?? {};
  const cellular = [c.apn && `APN ${c.apn}`, c.dnn && `DNN ${c.dnn}`, c.mtu && `MTU ${c.mtu}`, c.bootstrap_apn && `bootstrap APN ${c.bootstrap_apn}`].filter(Boolean);
  return (
    <dl className="kv ob-plan">
      <dt>Templates</dt>
      <dd>{plan.template_ids?.length ? plan.template_ids.map(templateName).join(", ") : "none"}</dd>
      <dt>Cellular</dt>
      <dd>{cellular.length ? cellular.join(" · ") : "unchanged"}</dd>
      <dt>Allowed networks</dt>
      <dd>{plan.allowed_plmns?.length ? plan.allowed_plmns.join(", ") : "any"}</dd>
      <dt>Discovery</dt>
      <dd>{plan.require_discovery ? "required first" : "not required"}</dd>
    </dl>
  );
}
