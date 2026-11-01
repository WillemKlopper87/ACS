// /onboarding/<tab>: sales on their way to service, CPEs nobody sold, and
// the sites and service plans a sale refers to (docs/ONBOARDING.md).
import type { ColumnDef } from "@tanstack/react-table";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { api, ApiError, type Onboarding as OnboardingRecord, type ServicePlan, type Site, type UnclaimedDevice } from "../api/client";
import type { ConfigTemplate } from "../api/types";
import { canManage } from "../auth/roles";
import { useAuth } from "../auth/useAuth";
import { DataTable } from "../components/DataTable";
import { OnboardingTimeline } from "../components/OnboardingTimeline";
import { OnboardingWizard, PlanSummary, type ClaimTarget } from "../components/OnboardingWizard";
import { SubNav, type SubNavItem } from "../components/SubNav";
import { devicePath } from "../lib/deviceSections";
import { fmtTime, timeAgo } from "../lib/format";
import { ONBOARDING_TABS, onboardingTab, parsePLMNs, STATE_LABEL, stateTone, type OnboardingTabId } from "../lib/onboarding";
import { toast } from "../lib/toast";
import { customerName, useCustomers } from "../lib/useCustomers";

type TabId = OnboardingTabId | "unclaimed" | "sites" | "plans";
const TAB_IDS: TabId[] = [...ONBOARDING_TABS.map((t) => t.id), "unclaimed", "sites", "plans"];

function errorText(e: unknown, fallback: string) {
  return e instanceof ApiError ? e.message : fallback;
}

export function Onboarding() {
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const fromPath = pathname.split("/")[2] as TabId;
  const tab: TabId = TAB_IDS.includes(fromPath) ? fromPath : "attention";
  const { role } = useAuth();
  const manage = canManage(role);
  const customers = useCustomers();

  const [onboardings, setOnboardings] = useState<OnboardingRecord[]>([]);
  const [unclaimed, setUnclaimed] = useState<UnclaimedDevice[]>([]);
  const [sites, setSites] = useState<Site[]>([]);
  const [plans, setPlans] = useState<ServicePlan[]>([]);
  const [templates, setTemplates] = useState<ConfigTemplate[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [wizard, setWizard] = useState<{ device?: ClaimTarget } | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [o, u, s, p] = await Promise.all([api.listOnboardings(), api.listUnclaimedDevices(), api.listSites(), api.listServicePlans()]);
      setOnboardings(o.items);
      setUnclaimed(u.items);
      setSites(s.items);
      setPlans(p.items);
    } catch (e) {
      setError(errorText(e, "Failed to reach the API — is cmd/api running?"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    api.listTemplates().then((r) => setTemplates(r.items), () => {});
  }, [load]);

  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const o of onboardings) c[onboardingTab(o.state)] = (c[onboardingTab(o.state)] ?? 0) + 1;
    return c;
  }, [onboardings]);

  const items: SubNavItem[] = [
    ...ONBOARDING_TABS.map((t) => ({ id: t.id, label: `${t.label}${counts[t.id] ? ` (${counts[t.id]})` : ""}`, to: `/onboarding/${t.id}` })),
    { id: "unclaimed", label: `Unclaimed${unclaimed.length ? ` (${unclaimed.length})` : ""}`, to: "/onboarding/unclaimed" },
    { id: "sites", label: "Sites", to: "/onboarding/sites", group: "Setup" },
    { id: "plans", label: "Service plans", to: "/onboarding/plans", group: "Setup" },
  ];

  const siteName = (id?: string | null) => (id ? sites.find((s) => s.id === id)?.name ?? "—" : "—");
  const planCode = (id?: string | null) => (id ? plans.find((p) => p.id === id)?.code ?? "—" : "—");
  const templateName = (id: string) => templates.find((t) => t.id === id)?.name ?? id.slice(0, 8);

  async function act(o: OnboardingRecord, action: "retry" | "cancel") {
    if (action === "cancel" && !window.confirm(`Cancel the onboarding of ${o.expected_serial}? The serial can then be sold again.`)) return;
    try {
      await (action === "retry" ? api.retryOnboarding(o.id) : api.cancelOnboarding(o.id));
      toast(action === "retry" ? "Checks will run again on the next check-in" : "Onboarding cancelled", "info");
      await load();
    } catch (e) {
      toast(errorText(e, `Failed to ${action}`), "error");
    }
  }

  const tabStates = ONBOARDING_TABS.find((t) => t.id === tab)?.states;
  const rows = useMemo(() => (tabStates ? onboardings.filter((o) => tabStates.includes(o.state)) : []), [onboardings, tabStates]);
  const current = rows.find((o) => o.id === selected) ?? null;

  const columns = useMemo<ColumnDef<OnboardingRecord, any>[]>(
    () => [
      { accessorKey: "expected_serial", header: "Serial" },
      {
        accessorKey: "state",
        header: "State",
        cell: ({ row }) => <span className={`pill ${stateTone(row.original.state)}`}>{STATE_LABEL[row.original.state]}</span>,
      },
      { accessorKey: "state_reason", header: "Waiting on", cell: ({ getValue }) => <span className="dim">{(getValue() as string) || "—"}</span> },
      { accessorKey: "customer_id", header: "Customer", cell: ({ getValue }) => customerName(customers, getValue() as string | null) },
      { accessorKey: "account_ref", header: "Account", cell: ({ getValue }) => (getValue() as string) || "—" },
      { accessorKey: "site_id", header: "Site", cell: ({ getValue }) => siteName(getValue() as string | null) },
      { accessorKey: "service_plan_id", header: "Plan", cell: ({ getValue }) => planCode(getValue() as string | null) },
      {
        accessorKey: "updated_at",
        header: "Changed",
        cell: ({ getValue }) => <span className="dim" title={`${fmtTime(getValue() as string)} UTC`}>{timeAgo(getValue() as string)}</span>,
      },
    ],
    // siteName/planCode read sites/plans.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [customers, sites, plans],
  );

  return (
    <section>
      {error && (
        <div className="banner error">
          {error} <button className="btn" onClick={load}>Retry</button>
        </div>
      )}
      <div className="ob-toolbar">
        <p className="dim" style={{ margin: 0 }}>
          A sale names the unit and SIM, who it is for, where it goes and what it gets. The ACS moves it to service as the CPE checks in.
        </p>
        <button className="btn primary" onClick={() => setWizard({})} disabled={!manage} title={manage ? undefined : "Needs the tenancy.manage permission"}>
          New sale
        </button>
      </div>

      <div className="split-workspace">
        <SubNav label="Onboarding" items={items} current={tab} />
        <div className="split-content">
          {loading && onboardings.length === 0 && <div className="loading">Loading onboarding…</div>}

          {tabStates && !loading && (
            <>
              <DataTable
                data={rows}
                columns={columns}
                getRowId={(o) => o.id}
                onRowClick={(o) => setSelected(o.id === selected ? null : o.id)}
                selectedRowId={selected ?? undefined}
                emptyMessage={tab === "attention" ? "Nothing needs attention." : "No onboardings here."}
              />
              {current && (
                <div className="panel ob-detail" aria-label={`Onboarding of ${current.expected_serial}`}>
                  <div className="ob-detail-head">
                    <h3>{current.expected_serial}</h3>
                    <div className="ob-actions">
                      {current.device_id && (
                        <Link className="btn" to={devicePath(current.device_id, "overview")}>
                          Open device
                        </Link>
                      )}
                      {(current.state === "ON_HOLD" || current.state === "FAILED") && (
                        <button className="btn primary" onClick={() => act(current, "retry")} disabled={!manage}>
                          Retry checks
                        </button>
                      )}
                      {current.state !== "IN_SERVICE" && current.state !== "CANCELLED" && (
                        <button className="btn danger" onClick={() => act(current, "cancel")} disabled={!manage}>
                          Cancel
                        </button>
                      )}
                    </div>
                  </div>
                  <OnboardingTimeline onboarding={current} />
                  <dl className="kv">
                    <dt>SIM (masked)</dt>
                    <dd>{[current.expected_iccid, current.expected_imsi].filter(Boolean).join(" · ") || "not checked"}</dd>
                    <dt>Role / model</dt>
                    <dd>
                      {current.role} · {current.commercial_model}
                    </dd>
                    <dt>Source</dt>
                    <dd>
                      {current.source}
                      {current.requested_by ? ` by ${current.requested_by}` : ""}
                    </dd>
                    {current.config_jobs.length > 0 && (
                      <>
                        <dt>Configuration jobs</dt>
                        <dd>{current.config_jobs.join(", ")}</dd>
                      </>
                    )}
                  </dl>
                </div>
              )}
            </>
          )}

          {tab === "unclaimed" && !loading && (
            <UnclaimedTable devices={unclaimed} manage={manage} onClaim={(d) => setWizard({ device: d })} />
          )}
          {tab === "sites" && !loading && <SitesTab sites={sites} customers={customers} manage={manage} onCreated={load} />}
          {tab === "plans" && !loading && (
            <PlansTab plans={plans} templates={templates} customers={customers} manage={manage} templateName={templateName} onCreated={load} />
          )}
        </div>
      </div>

      {wizard && (
        <OnboardingWizard
          device={wizard.device}
          onClose={() => setWizard(null)}
          onCreated={(o) => {
            setWizard(null);
            toast(`Sale recorded for ${o.expected_serial}`, "success");
            setSelected(o.id);
            navigate(`/onboarding/${onboardingTab(o.state)}`);
            load();
          }}
        />
      )}
    </section>
  );
}

function UnclaimedTable({ devices, manage, onClaim }: { devices: UnclaimedDevice[]; manage: boolean; onClaim: (d: UnclaimedDevice) => void }) {
  const columns = useMemo<ColumnDef<UnclaimedDevice, any>[]>(
    () => [
      {
        accessorKey: "serial_number",
        header: "Serial",
        cell: ({ row }) => <Link to={devicePath(row.original.id, "overview")}>{row.original.serial_number}</Link>,
      },
      { accessorKey: "manufacturer", header: "Vendor" },
      { accessorKey: "product_class", header: "Model" },
      { accessorKey: "online_status", header: "Status" },
      {
        accessorKey: "last_inform_at",
        header: "Last check-in",
        cell: ({ getValue }) => <span className="dim">{getValue() ? timeAgo(getValue() as string) : "never"}</span>,
      },
      {
        id: "claim",
        header: "",
        cell: ({ row }) => (
          <button className="btn" onClick={() => onClaim(row.original)} disabled={!manage}>
            Claim
          </button>
        ),
      },
    ],
    [manage, onClaim],
  );
  return (
    <>
      <p className="dim ob-hint">CPEs that checked in with no sale on them. Claim one to give it a customer, site and plan.</p>
      <DataTable data={devices} columns={columns} getRowId={(d) => d.id} emptyMessage="Every connected CPE belongs to a sale." />
    </>
  );
}

function SitesTab({ sites, customers, manage, onCreated }: {
  sites: Site[];
  customers: ReturnType<typeof useCustomers>;
  manage: boolean;
  onCreated: () => Promise<void>;
}) {
  const [form, setForm] = useState({ name: "", customer_id: "", external_ref: "", street: "", suburb: "", city: "", postal_code: "", country: "ZA" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setForm({ ...form, [k]: e.target.value });

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.createSite({ ...form, customer_id: form.customer_id || null, external_ref: form.external_ref || null });
      toast(`Site "${form.name}" created`, "success");
      setForm({ ...form, name: "", external_ref: "", street: "", suburb: "" });
      await onCreated();
    } catch (err) {
      setError(errorText(err, "Failed to create the site"));
    } finally {
      setBusy(false);
    }
  }

  const columns = useMemo<ColumnDef<Site, any>[]>(
    () => [
      { accessorKey: "name", header: "Name" },
      { accessorKey: "customer_id", header: "Customer", cell: ({ getValue }) => customerName(customers, getValue() as string | null) },
      { id: "address", header: "Address", cell: ({ row }) => [row.original.street, row.original.suburb, row.original.city].filter(Boolean).join(", ") || "—" },
      { accessorKey: "external_ref", header: "BSS reference", cell: ({ getValue }) => (getValue() as string) || "—" },
    ],
    [customers],
  );

  return (
    <>
      <DataTable data={sites} columns={columns} getRowId={(s) => s.id} emptyMessage="No sites yet." />
      <form className="panel" style={{ marginTop: "1rem" }} onSubmit={submit}>
        <h3>New site</h3>
        <div className="form-row">
          <label className="field">
            <span>Name</span>
            <input value={form.name} onChange={set("name")} required placeholder="e.g. 14 Oak Ave" />
          </label>
          <label className="field">
            <span>Customer</span>
            <select value={form.customer_id} onChange={set("customer_id")}>
              <option value="">No customer</option>
              {customers.map((c) => (
                <option key={c.id} value={c.id}>{c.name}</option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>BSS premise reference</span>
            <input value={form.external_ref} onChange={set("external_ref")} />
          </label>
        </div>
        <div className="form-row">
          <label className="field">
            <span>Street</span>
            <input value={form.street} onChange={set("street")} />
          </label>
          <label className="field">
            <span>Suburb</span>
            <input value={form.suburb} onChange={set("suburb")} />
          </label>
          <label className="field">
            <span>City</span>
            <input value={form.city} onChange={set("city")} />
          </label>
          <label className="field">
            <span>Postal code</span>
            <input value={form.postal_code} onChange={set("postal_code")} />
          </label>
        </div>
        {error && <div className="banner error" style={{ marginTop: "0.6rem" }}>{error}</div>}
        <div className="form-row">
          <button type="submit" className="btn primary" disabled={busy || !manage || !form.name.trim()}>
            {busy ? "Creating…" : "Create site"}
          </button>
        </div>
      </form>
    </>
  );
}

function PlansTab({ plans, templates, customers, manage, templateName, onCreated }: {
  plans: ServicePlan[];
  templates: ConfigTemplate[];
  customers: ReturnType<typeof useCustomers>;
  manage: boolean;
  templateName: (id: string) => string;
  onCreated: () => Promise<void>;
}) {
  const [form, setForm] = useState({ code: "", name: "", customer_id: "", apn: "", dnn: "", mtu: "", bootstrap_apn: "", plmns: "", discovery: false });
  const [templateIds, setTemplateIds] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setForm({ ...form, [k]: e.target.value });
  const { plmns, invalid } = parsePLMNs(form.plmns);
  const usableTemplates = templates.filter((t) => !t.customer_id || t.customer_id === form.customer_id);
  const cellularSet = Boolean(form.apn || form.dnn || form.mtu || form.bootstrap_apn);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.createServicePlan({
        code: form.code.trim(),
        name: form.name.trim(),
        customer_id: form.customer_id || null,
        template_ids: templateIds.filter((id) => usableTemplates.some((t) => t.id === id)),
        cellular: { apn: form.apn || undefined, dnn: form.dnn || undefined, mtu: form.mtu ? Number(form.mtu) : undefined, bootstrap_apn: form.bootstrap_apn || undefined },
        allowed_plmns: plmns,
        require_discovery: form.discovery,
      });
      toast(`Plan ${form.code} created`, "success");
      setForm({ code: "", name: "", customer_id: form.customer_id, apn: "", dnn: "", mtu: "", bootstrap_apn: "", plmns: "", discovery: false });
      setTemplateIds([]);
      await onCreated();
    } catch (err) {
      setError(errorText(err, "Failed to create the plan"));
    } finally {
      setBusy(false);
    }
  }

  const columns = useMemo<ColumnDef<ServicePlan, any>[]>(
    () => [
      { accessorKey: "code", header: "Code" },
      { accessorKey: "name", header: "Name" },
      { accessorKey: "customer_id", header: "Customer", cell: ({ getValue }) => (getValue() ? customerName(customers, getValue() as string) : "shared") },
      { id: "apn", header: "APN", cell: ({ row }) => row.original.cellular?.apn || "—" },
      { id: "networks", header: "Allowed networks", cell: ({ row }) => row.original.allowed_plmns?.join(", ") || "any" },
      { id: "templates", header: "Templates", cell: ({ row }) => row.original.template_ids?.length ?? 0 },
    ],
    [customers],
  );
  const shown = plans.find((p) => p.id === open);

  return (
    <>
      <DataTable data={plans} columns={columns} getRowId={(p) => p.id} onRowClick={(p) => setOpen(p.id === open ? null : p.id)} selectedRowId={open ?? undefined} emptyMessage="No service plans yet." />
      {shown && (
        <div className="panel" style={{ marginTop: "0.8rem" }}>
          <h3>
            {shown.code} — {shown.name}
          </h3>
          <PlanSummary plan={shown} templateName={templateName} />
        </div>
      )}
      <form className="panel" style={{ marginTop: "1rem" }} onSubmit={submit}>
        <h3>New service plan</h3>
        <p className="dim ob-hint">What a sale means for the CPE. The code is the BSS service plan code.</p>
        <div className="form-row">
          <label className="field">
            <span>Code</span>
            <input value={form.code} onChange={set("code")} required placeholder="e.g. HOME-5G-100" />
          </label>
          <label className="field">
            <span>Name</span>
            <input value={form.name} onChange={set("name")} required />
          </label>
          <label className="field">
            <span>Customer</span>
            <select value={form.customer_id} onChange={set("customer_id")}>
              <option value="">Shared by every customer</option>
              {customers.map((c) => (
                <option key={c.id} value={c.id}>{c.name}</option>
              ))}
            </select>
          </label>
        </div>
        <fieldset className="ob-fieldset">
          <legend>Cellular profile</legend>
          <div className="form-row">
            <label className="field">
              <span>APN</span>
              <input value={form.apn} onChange={set("apn")} />
            </label>
            <label className="field">
              <span>DNN (5G SA)</span>
              <input value={form.dnn} onChange={set("dnn")} />
            </label>
            <label className="field">
              <span>MTU</span>
              <input value={form.mtu} onChange={set("mtu")} inputMode="numeric" pattern="\d*" />
            </label>
            <label className="field">
              <span>Bootstrap APN</span>
              <input value={form.bootstrap_apn} onChange={set("bootstrap_apn")} />
            </label>
          </div>
          <p className="dim ob-hint">
            Written last, only where the CPE has the field and its value differs. A wrong APN cuts the CPE off from the ACS, so check
            it on one unit first.
          </p>
        </fieldset>
        <div className="form-row">
          <label className="field">
            <span>Allowed networks (MCC+MNC)</span>
            <input value={form.plmns} onChange={set("plmns")} placeholder="e.g. 65506 — empty allows any SIM" aria-invalid={invalid.length > 0} />
            {invalid.length > 0 && <span className="ob-invalid">Not MCC+MNC (5–6 digits): {invalid.join(", ")}</span>}
          </label>
          <label className="field inline">
            <input type="checkbox" checked={form.discovery || cellularSet} disabled={cellularSet} onChange={(e) => setForm({ ...form, discovery: e.target.checked })} />
            <span>Wait for parameter discovery{cellularSet ? " (needed for the cellular profile)" : ""}</span>
          </label>
        </div>
        {usableTemplates.length > 0 && (
          <fieldset className="ob-fieldset">
            <legend>Templates, applied in this order</legend>
            {usableTemplates.map((t) => (
              <label key={t.id} className="field inline">
                <input
                  type="checkbox"
                  checked={templateIds.includes(t.id)}
                  onChange={(e) => setTemplateIds(e.target.checked ? [...templateIds, t.id] : templateIds.filter((id) => id !== t.id))}
                />
                <span>
                  {t.name} <span className="dim">({t.parameters.length} parameters)</span>
                </span>
              </label>
            ))}
          </fieldset>
        )}
        {error && <div className="banner error" style={{ marginTop: "0.6rem" }}>{error}</div>}
        <div className="form-row">
          <button type="submit" className="btn primary" disabled={busy || !manage || !form.code.trim() || !form.name.trim() || invalid.length > 0}>
            {busy ? "Creating…" : "Create plan"}
          </button>
        </div>
      </form>
    </>
  );
}
