import { useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import { toast } from "../lib/toast";

type Evidence = {
  discoveredAt: string | null;
  standardCount: number;
  vendorCount: number;
  capabilities: Record<string, unknown>;
  recommendations: string[];
  stages: Record<string, boolean>;
  tables: string[];
  changes: number;
  checks: { category: string; status: string; evidence: string; action?: string }[];
  receivers: { receiver_id: string; state: string; alert?: string }[];
};

export function DeviceCompatibility({ id, writable }: { id: string; writable: boolean }) {
  const [evidence, setEvidence] = useState<Evidence | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { setEvidence(null); setError(null); }, [id]);

  async function inspect() {
    setBusy(true); setError(null);
    try {
      const [assessment, connection, compatibility, telemetry] = await Promise.all([api.getStandardsAssessment(id), api.getConnectionDiagnostics(id), api.getCompatibility(id), api.getTelemetryHealth(id)]);
      const paths = (assessment.paths ?? []) as { path: string; classification: string }[];
      const changes = compatibility.capability_changes as Record<string, string[]> | undefined;
      setEvidence({
        discoveredAt: (assessment.discovered_at as string | null) ?? null,
        standardCount: paths.filter(p => p.classification === "standard").length,
        vendorCount: paths.filter(p => p.classification === "vendor_extension").length,
        capabilities: (assessment.capabilities ?? {}) as Record<string, unknown>,
        recommendations: (connection.recommendations ?? []) as string[],
        stages: { connected: compatibility.connected, discovered: compatibility.discovered, read_tested: compatibility.read_tested, write_tested: compatibility.write_tested, firmware_qualified: compatibility.firmware_qualified },
        tables: [...new Set(paths.filter(p => p.path.includes("{i}")).map(p => p.path.split("{i}")[0]))].slice(0, 32),
        changes: Object.values(changes ?? {}).reduce((sum, values) => sum + values.length, 0),
        checks: (connection.checks ?? []) as Evidence["checks"],
        receivers: (telemetry.receivers ?? []) as Evidence["receivers"],
      });
    } catch (e) { setError(e instanceof ApiError ? e.message : "Could not load compatibility evidence"); }
    finally { setBusy(false); }
  }

  async function discover() {
    setBusy(true);
    try {
      const job = await api.discoverParameters(id);
      toast(`Discovery queued: ${job.command_key}. Check Recent Jobs, then refresh evidence.`, "success");
    } catch (e) { toast(e instanceof ApiError ? e.message : "Could not queue discovery", "error"); }
    finally { setBusy(false); }
  }

  async function exportEvidence() {
    setBusy(true);
    try { await api.exportDiscovery(id); }
    catch (e) { toast(e instanceof ApiError ? e.message : "Could not export discovery", "error"); }
    finally { setBusy(false); }
  }

  async function findInstances() {
    if (!evidence?.tables.length) return;
    setBusy(true);
    try { await api.discoverInstances(id, evidence.tables); toast("Instance discovery queued. Refresh evidence after the job completes.", "success"); }
    catch (e) { toast(e instanceof ApiError ? e.message : "Could not queue instance discovery", "error"); }
    finally { setBusy(false); }
  }

  async function renewReceiver(receiverId: string) {
    setBusy(true);
    try { await api.renewBulkReceiver(id, receiverId); toast("Receiver renewal queued for the device.", "success"); }
    catch (e) { toast(e instanceof ApiError ? e.message : "Could not renew receiver", "error"); }
    finally { setBusy(false); }
  }

  return <section className="panel" aria-label="Device compatibility">
    <h3>Device compatibility</h3>
    <p>Discover the device’s supported parameters and save its firmware evidence before provisioning.</p>
    <div className="form-row">
      <button className="btn" disabled={busy || !writable} onClick={discover}>Discover capabilities</button>
      <button className="btn" disabled={busy} onClick={inspect}>Refresh evidence</button>
      <button className="btn" disabled={busy} onClick={exportEvidence}>Export discovery</button>
      <button className="btn" disabled={busy || !writable || !evidence?.tables.length} onClick={findInstances}>Find existing instances</button>
    </div>
    {error && <p role="alert">{error}</p>}
    {evidence && <div aria-live="polite">
      <p>{evidence.discoveredAt ? `Last discovery: ${new Date(evidence.discoveredAt).toLocaleString()}` : "No completed discovery recorded."}</p>
      <p>{evidence.standardCount} standard paths · {evidence.vendorCount} vendor extensions</p>
      <dl className="detail-grid">
        {[["Contact recorded", "connected"], ["Discovered", "discovered"], ["Reads tested", "read_tested"], ["Writes verified", "write_tested"], ["Firmware qualified", "firmware_qualified"]].map(([label, key]) =>
          <div key={key}><dt>{label}</dt><dd>{evidence.stages[key] ? "Evidence recorded" : "Not established"}</dd></div>)}
      </dl>
      {evidence.changes > 0 && <p>{evidence.changes} capability changes since the previous discovery.</p>}
      <dl className="detail-grid">
        {[["Cellular", "cellular"], ["Wi-Fi", "wifi"], ["Mesh topology data", "mesh_data_elements"], ["Bulk data", "bulk_data"]].map(([label, key]) =>
          <div key={key}><dt>{label}</dt><dd>{evidence.capabilities[key] ? "Advertised by device" : "Not discovered"}</dd></div>)}
      </dl>
      {evidence.recommendations.map(text => <p key={text}>{text}</p>)}
      {evidence.checks.map(check => <p key={check.category}>{check.category.replaceAll("_", " ")}: {check.status}. {check.action}</p>)}
      {evidence.receivers.map(receiver => <div key={receiver.receiver_id}>
        <p>Telemetry: {receiver.state.replaceAll("_", " ")}. {receiver.alert}</p>
        {receiver.state !== "revoked" && <button className="btn" disabled={busy || !writable} onClick={() => renewReceiver(receiver.receiver_id)}>Renew collection credentials</button>}
      </div>)}
      <p>Discovery records advertised capabilities. Hardware qualification remains specific to the installed firmware.</p>
    </div>}
  </section>;
}
