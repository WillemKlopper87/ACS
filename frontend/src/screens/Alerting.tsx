import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { AlertIncident, AlertPolicy } from "../api/types";

export function Alerting() {
  const [policies,setPolicies]=useState<AlertPolicy[]>([]); const [incidents,setIncidents]=useState<AlertIncident[]>([]); const [error,setError]=useState("");
  const load=async()=>{try{const [p,i]=await Promise.all([api.listAlertPolicies(),api.listAlertIncidents("open")]);setPolicies(p.items);setIncidents(i.items);setError("")}catch{setError("Failed to load alerting data")}};
  useEffect(()=>{void load()},[]);
  const acknowledge=async(id:string)=>{await api.updateAlertIncident(id,"acknowledged");await load()};
  return <section><div className="page-head"><div><h1>Alerting & escalation</h1><p className="dim">Policy precedence: device → group → tenant → fleet.</p></div><button className="btn" onClick={()=>void load()}>Refresh</button></div>{error&&<div className="banner error">{error}</div>}<div className="panel"><h3>Active incidents</h3>{incidents.length===0?<p className="dim">No open incidents.</p>:<div className="table-wrap"><table><thead><tr><th>Priority</th><th>Tenant</th><th>Device</th><th>Summary</th><th>Stage</th><th/></tr></thead><tbody>{incidents.map(i=><tr key={i.id}><td><span className={`pill ${i.priority === "P1" ? "pill-bad" : "pill-warn"}`}>{i.priority}</span></td><td>{i.tenant_id}</td><td className="mono">{i.device_id}</td><td>{i.summary}</td><td>{i.escalation_stage}</td><td><button className="btn sm" onClick={()=>void acknowledge(i.id)}>Acknowledge</button></td></tr>)}</tbody></table></div>}</div><div className="panel"><h3>Configured policies</h3>{policies.length===0?<p className="dim">No alert policies configured.</p>:<div className="table-wrap"><table><thead><tr><th>Name</th><th>Scope</th><th>Tier</th><th>Offline threshold</th></tr></thead><tbody>{policies.map(p=><tr key={p.id}><td>{p.name}</td><td>{p.scope}</td><td>{p.customer_tier||"—"}</td><td>{p.offline_after||"—"}</td></tr>)}</tbody></table></div>}</div></section>;
}
