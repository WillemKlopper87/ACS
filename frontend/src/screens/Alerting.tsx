import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { AlertIncident, AlertPolicy } from "../api/types";

type DraftStep = { after: string; destination: string; recipient: string };
const emptyStep = (): DraftStep => ({ after: "900", destination: "nms", recipient: "" });

export function Alerting() {
  const [policies, setPolicies] = useState<AlertPolicy[]>([]);
  const [incidents, setIncidents] = useState<AlertIncident[]>([]);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [scope, setScope] = useState<AlertPolicy["scope"]>("fleet");
  const [target, setTarget] = useState("");
  const [priority, setPriority] = useState("P2");
  const [offline, setOffline] = useState("900");
  const [steps, setSteps] = useState<DraftStep[]>([emptyStep()]);

  const load = async () => {
    try { const [p, i] = await Promise.all([api.listAlertPolicies(), api.listAlertIncidents("open")]); setPolicies(p.items); setIncidents(i.items); setError(""); }
    catch { setError("Failed to load alerting data"); }
  };
  useEffect(() => { void load(); }, []);
  const changeState = async (id: string, state: string) => { try { await api.updateAlertIncident(id, state); await load(); } catch { setError(`Failed to ${state} incident`); } };
  const updateStep = (index: number, field: keyof DraftStep, value: string) => setSteps(current => current.map((step, i) => i === index ? { ...step, [field]: value } : step));
  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await api.createAlertPolicy({ name, scope, ...(scope === "fleet" ? {} : scope === "device" ? { device_id: target } : scope === "group" ? { group_id: target } : { tenant_id: target }), fault_priorities: { offline: priority }, offline_after: Number(offline) * 1e9, steps: steps.map(step => ({ after: Number(step.after) * 1e9, destination: step.destination, recipient: step.recipient })) });
      setName(""); setTarget(""); setSteps([emptyStep()]); await load();
    } catch { setError("Failed to create alert policy"); }
  };
  return <section>
    <div className="page-head"><div><h1>Alerting & escalation</h1><p className="dim">Policy precedence: device → group → tenant → fleet.</p></div><button className="btn" onClick={() => void load()}>Refresh</button></div>
    {error && <div className="banner error">{error}</div>}
    <div className="panel"><h3>Create alert policy</h3><form onSubmit={create}>
      <div className="form-row"><label className="field"><span>Name</span><input value={name} onChange={e => setName(e.target.value)} required /></label><label className="field"><span>Scope</span><select value={scope} onChange={e => setScope(e.target.value as AlertPolicy["scope"])}><option value="fleet">Fleet</option><option value="tenant">Tenant</option><option value="group">Device group</option><option value="device">Individual CPE</option></select></label></div>
      {scope !== "fleet" && <label className="field"><span>{scope} ID</span><input value={target} onChange={e => setTarget(e.target.value)} required /></label>}
      <div className="form-row"><label className="field"><span>Offline priority</span><select value={priority} onChange={e => setPriority(e.target.value)}><option>P1</option><option>P2</option><option>P3</option></select></label><label className="field"><span>Offline threshold (seconds)</span><input type="number" min="0" value={offline} onChange={e => setOffline(e.target.value)} required /></label></div>
      <h4>Escalation steps</h4>
      {steps.map((step, index) => <div className="form-row" key={index}><label className="field"><span>Step {index + 1} after (seconds)</span><input type="number" min="0" value={step.after} onChange={e => updateStep(index, "after", e.target.value)} required /></label><label className="field"><span>Destination</span><input value={step.destination} onChange={e => updateStep(index, "destination", e.target.value)} placeholder="nms, email, sms" required /></label><label className="field"><span>Recipient</span><input value={step.recipient} onChange={e => updateStep(index, "recipient", e.target.value)} placeholder="on-call identifier" required /></label>{steps.length > 1 && <button type="button" className="btn sm" onClick={() => setSteps(current => current.filter((_, i) => i !== index))}>Remove</button>}</div>)}
      {steps.length < 5 && <button type="button" className="btn sm" onClick={() => setSteps(current => [...current, emptyStep()])}>Add escalation step</button>}
      <div><button className="btn primary" type="submit" disabled={!name || (scope !== "fleet" && !target)}>Create policy</button></div>
    </form></div>
    <div className="panel"><h3>Active incidents</h3>{incidents.length === 0 ? <p className="dim">No open incidents.</p> : <div className="table-wrap"><table><thead><tr><th>Priority</th><th>Tenant</th><th>Device</th><th>Summary</th><th>Stage</th><th /></tr></thead><tbody>{incidents.map(i => <tr key={i.id}><td><span className={`pill ${i.priority === "P1" ? "pill-bad" : "pill-warn"}`}>{i.priority}</span></td><td>{i.tenant_id}</td><td className="mono">{i.device_id}</td><td>{i.summary}</td><td>{i.escalation_stage}</td><td><button className="btn sm" onClick={() => void changeState(i.id, "acknowledged")}>Acknowledge</button> <button className="btn sm" onClick={() => void changeState(i.id, "suppressed")}>Suppress</button> <button className="btn sm" onClick={() => void changeState(i.id, "recovered")}>Recover</button></td></tr>)}</tbody></table></div>}</div>
    <div className="panel"><h3>Configured policies</h3>{policies.length === 0 ? <p className="dim">No alert policies configured.</p> : <div className="table-wrap"><table><thead><tr><th>Name</th><th>Scope</th><th>Tier</th><th>Offline threshold</th><th>Steps</th></tr></thead><tbody>{policies.map(p => <tr key={p.id}><td>{p.name}</td><td>{p.scope}</td><td>{p.customer_tier || "—"}</td><td>{p.offline_after || "—"}</td><td>{p.steps?.length || 0}</td></tr>)}</tbody></table></div>}</div>
  </section>;
}
