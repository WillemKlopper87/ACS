import { useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { BSSAdapterCallResult, BSSHealth, BSSMapping, BSSOAuthClient, BSSStats, BSSWebhookSubscription } from "../api/types";
import { toast } from "../lib/toast";

// BSS integration admin panel (admin-platform backlog): a superadmin view
// onto the BSS-facing adapter (internal/bss, cmd/bssadapter — see
// bss-integration-guide.md). Three sections per the agreed design: (1)
// onboarding/setup — the real account-device mappings and webhook
// subscriptions, editable here directly against the same tables
// cmd/bssadapter itself reads/writes; (2) health — real aggregate counts
// plus a live reachability check against the running bssadapter process;
// (3) troubleshooting — scripts that call the *live* bssadapter over HTTP
// (not this repository layer) so what's shown is the actual request/
// response flow a BSS integrator would see, auth and faults included.
type Tab = "setup" | "health" | "troubleshoot";

function statusPillClass(status: string) {
  if (status === "ACTIVE") return "pill pill-ok";
  if (status === "SUSPENDED") return "pill pill-warn";
  if (status === "TERMINATED") return "pill pill-danger";
  return "pill pill-pending"; // PENDING_ACTIVE
}

function maskSecret(secret: string) {
  if (secret.length <= 8) return "•".repeat(secret.length);
  return `${secret.slice(0, 4)}${"•".repeat(6)}${secret.slice(-2)}`;
}

function ResultCard({ result }: { result: BSSAdapterCallResult }) {
  const ok = result.status_code >= 200 && result.status_code < 300;
  const tone = result.error ? "pill-danger" : ok ? "pill-ok" : result.status_code === 401 ? "pill-warn" : "pill-danger";
  return (
    <div className="panel" style={{ marginTop: "0.75rem", background: "var(--surface-2)" }}>
      <p className="dim" style={{ margin: "0 0 0.4rem", fontSize: "0.78rem" }}>{result.description}</p>
      <div className="form-row" style={{ marginTop: 0, alignItems: "center" }}>
        <span className={`pill ${tone}`}>{result.error ? "UNREACHABLE" : result.status_code}</span>
        <span className="dim mono" style={{ fontSize: "0.76rem" }}>{result.method} {result.url}</span>
        <span className="dim" style={{ fontSize: "0.76rem", marginLeft: "auto" }}>{result.latency_ms}ms</span>
      </div>
      <pre style={{ marginTop: "0.6rem", fontSize: "0.76rem", overflowX: "auto", background: "var(--surface)", border: "1px solid var(--border)", borderRadius: "var(--radius-md)", padding: "0.6rem" }}>
        {result.error ?? result.body}
      </pre>
    </div>
  );
}

export function BSSIntegration() {
  const [tab, setTab] = useState<Tab>("setup");

  // --- setup: mappings ---
  const [mappings, setMappings] = useState<BSSMapping[]>([]);
  const [mappingAccountId, setMappingAccountId] = useState("");
  const [mappingOuiSerial, setMappingOuiSerial] = useState("");
  const [mappingServicePlan, setMappingServicePlan] = useState("");

  // --- setup: webhooks ---
  const [webhooks, setWebhooks] = useState<BSSWebhookSubscription[]>([]);
  const [webhookAccountId, setWebhookAccountId] = useState("");
  const [webhookTargetUrl, setWebhookTargetUrl] = useState("");
  const [webhookSecret, setWebhookSecret] = useState("");

  // --- setup: OAuth2 clients ---
  const [oauthClients, setOauthClients] = useState<BSSOAuthClient[]>([]);
  const [oauthClientName, setOauthClientName] = useState("");
  const [newClientSecret, setNewClientSecret] = useState<{ clientId: string; secret: string } | null>(null);

  // --- health ---
  const [stats, setStats] = useState<BSSStats | null>(null);
  const [health, setHealth] = useState<BSSHealth | null>(null);
  const [checkingHealth, setCheckingHealth] = useState(false);

  // --- troubleshooting ---
  const [lookupAccountId, setLookupAccountId] = useState("");
  const [lookupResult, setLookupResult] = useState<BSSAdapterCallResult | null>(null);
  const [authResult, setAuthResult] = useState<{ with_token: BSSAdapterCallResult; without_token: BSSAdapterCallResult } | null>(null);
  const [commandKey, setCommandKey] = useState("");
  const [jobResult, setJobResult] = useState<BSSAdapterCallResult | null>(null);
  const [orderAccountId, setOrderAccountId] = useState("");
  const [orderId, setOrderId] = useState("");
  const [orderSsid, setOrderSsid] = useState("");
  const [orderResult, setOrderResult] = useState<BSSAdapterCallResult | null>(null);
  const [running, setRunning] = useState<string | null>(null);

  async function loadSetup() {
    try {
      const [m, w, o] = await Promise.all([api.listBSSMappings(), api.listBSSWebhooks(), api.listBSSOAuthClients()]);
      setMappings(m.items);
      setWebhooks(w.items);
      setOauthClients(o.items);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to load BSS setup data", "error");
    }
  }

  async function createOAuthClient(e: React.FormEvent) {
    e.preventDefault();
    try {
      const result = await api.createBSSOAuthClient(oauthClientName);
      setOauthClientName("");
      setNewClientSecret({ clientId: result.client.client_id, secret: result.client_secret });
      toast("OAuth2 client registered", "success");
      await loadSetup();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to create OAuth2 client", "error");
    }
  }

  async function revokeOAuthClient(id: string) {
    try {
      await api.revokeBSSOAuthClient(id);
      await loadSetup();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to revoke OAuth2 client", "error");
    }
  }

  async function loadHealth() {
    setCheckingHealth(true);
    try {
      const [s, h] = await Promise.all([api.getBSSStats(), api.getBSSHealth()]);
      setStats(s);
      setHealth(h);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to load BSS health", "error");
    } finally {
      setCheckingHealth(false);
    }
  }

  useEffect(() => {
    loadSetup();
    loadHealth();
  }, []);

  async function createMapping(e: React.FormEvent) {
    e.preventDefault();
    try {
      await api.createBSSMapping(mappingAccountId, mappingOuiSerial, mappingServicePlan || undefined);
      setMappingAccountId("");
      setMappingOuiSerial("");
      setMappingServicePlan("");
      toast("Mapping created", "success");
      await loadSetup();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to create mapping", "error");
    }
  }

  async function createWebhook(e: React.FormEvent) {
    e.preventDefault();
    try {
      await api.createBSSWebhook({
        account_id: webhookAccountId || undefined,
        target_url: webhookTargetUrl,
        secret: webhookSecret,
        event_types: ["JOB_COMPLETED"],
      });
      setWebhookAccountId("");
      setWebhookTargetUrl("");
      setWebhookSecret("");
      toast("Webhook subscription created", "success");
      await loadSetup();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to create webhook subscription", "error");
    }
  }

  async function deleteWebhook(id: string) {
    try {
      await api.deleteBSSWebhook(id);
      await loadSetup();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to delete webhook subscription", "error");
    }
  }

  async function runMappingLookup(e: React.FormEvent) {
    e.preventDefault();
    setRunning("lookup");
    try {
      setLookupResult(await api.troubleshootMappingLookup(lookupAccountId));
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Script failed to run", "error");
    } finally {
      setRunning(null);
    }
  }

  async function runAuthCheck() {
    setRunning("auth");
    try {
      setAuthResult(await api.troubleshootAuthCheck());
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Script failed to run", "error");
    } finally {
      setRunning(null);
    }
  }

  async function runJobStatus(e: React.FormEvent) {
    e.preventDefault();
    setRunning("job");
    try {
      setJobResult(await api.troubleshootJobStatus(commandKey));
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Script failed to run", "error");
    } finally {
      setRunning(null);
    }
  }

  async function runOrderDispatch(e: React.FormEvent) {
    e.preventDefault();
    setRunning("order");
    try {
      setOrderResult(await api.troubleshootOrderDispatch({
        external_order_id: orderId,
        account_id: orderAccountId,
        wifi_ssid: orderSsid || undefined,
      }));
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Script failed to run", "error");
    } finally {
      setRunning(null);
    }
  }

  return (
    <section>
      <div className="form-row" style={{ marginTop: 0 }}>
        {(["setup", "health", "troubleshoot"] as Tab[]).map((t) => (
          <button key={t} className={tab === t ? "btn primary" : "btn"} onClick={() => setTab(t)}>
            {t === "setup" ? "Onboarding / Setup" : t === "health" ? "Health" : "Troubleshooting"}
          </button>
        ))}
      </div>

      {tab === "setup" && (
        <div className="split two-col">
          <div className="panel">
            <h3>Account–device mappings</h3>
            <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
              Workflow A. <code>oui_serial</code> must belong to a device that has already sent at least one Inform.
            </p>
            <form onSubmit={createMapping} className="form-row" style={{ flexWrap: "wrap" }}>
              <input placeholder="account_id" value={mappingAccountId} onChange={(e) => setMappingAccountId(e.target.value)} required />
              <input placeholder="oui_serial" value={mappingOuiSerial} onChange={(e) => setMappingOuiSerial(e.target.value)} required />
              <input placeholder="service_plan (optional)" value={mappingServicePlan} onChange={(e) => setMappingServicePlan(e.target.value)} />
              <button type="submit" className="btn primary">Create</button>
            </form>
            {mappings.length === 0 ? (
              <p className="dim" style={{ fontSize: "0.84rem" }}>No mappings yet.</p>
            ) : (
              <div style={{ maxHeight: "22rem", overflowY: "auto", marginTop: "0.75rem" }}>
                {mappings.map((m) => (
                  <div className="param-row" key={m.id}>
                    <span className="path">{m.account_id} <span className="dim">→</span> {m.oui_serial}</span>
                    <span className={statusPillClass(m.status)}>{m.status}</span>
                    {m.service_plan && <span className="src">{m.service_plan}</span>}
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="panel">
            <h3>Webhook subscriptions</h3>
            <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
              JOB_COMPLETED delivery targets. Leave account_id blank for a fleet-wide subscription.
            </p>
            <form onSubmit={createWebhook} className="form-row" style={{ flexWrap: "wrap" }}>
              <input placeholder="account_id (optional)" value={webhookAccountId} onChange={(e) => setWebhookAccountId(e.target.value)} />
              <input placeholder="https://target-url" value={webhookTargetUrl} onChange={(e) => setWebhookTargetUrl(e.target.value)} required />
              <input placeholder="HMAC secret" value={webhookSecret} onChange={(e) => setWebhookSecret(e.target.value)} required />
              <button type="submit" className="btn primary">Create</button>
            </form>
            {webhooks.length === 0 ? (
              <p className="dim" style={{ fontSize: "0.84rem" }}>No webhook subscriptions yet.</p>
            ) : (
              <div style={{ maxHeight: "22rem", overflowY: "auto", marginTop: "0.75rem" }}>
                {webhooks.map((w) => (
                  <div className="param-row" key={w.id}>
                    <span className="path">{w.account_id ?? "fleet-wide"} <span className="dim">→</span> {w.target_url}</span>
                    <button className="btn danger sm" onClick={() => deleteWebhook(w.id)}>
                      Delete
                    </button>
                    <span className="src mono">secret {maskSecret(w.secret)} · events {w.event_types.join(", ")}</span>
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="panel">
            <h3>OAuth2 clients</h3>
            <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
              Recommended auth for new integrations (RFC 6749 client-credentials). The secret is shown once, right
              after creation — copy it now, it can't be retrieved again.
            </p>
            <form onSubmit={createOAuthClient} className="form-row" style={{ flexWrap: "wrap" }}>
              <input placeholder="Integration name, e.g. Salesforce Comm Cloud" value={oauthClientName} onChange={(e) => setOauthClientName(e.target.value)} required />
              <button type="submit" className="btn primary">Register</button>
            </form>
            {newClientSecret && (
              <div className="panel" style={{ marginTop: "0.75rem", background: "var(--surface-2)" }}>
                <p className="dim" style={{ margin: "0 0 0.4rem", fontSize: "0.78rem" }}>
                  Save this now — it will not be shown again.
                </p>
                <div className="param-row">
                  <span className="path">client_id</span>
                  <span className="val mono">{newClientSecret.clientId}</span>
                </div>
                <div className="param-row">
                  <span className="path">client_secret</span>
                  <span className="val mono">{newClientSecret.secret}</span>
                </div>
                <button className="btn" style={{ marginTop: "0.5rem" }} onClick={() => setNewClientSecret(null)}>
                  Dismiss
                </button>
              </div>
            )}
            {oauthClients.length === 0 ? (
              <p className="dim" style={{ fontSize: "0.84rem" }}>No OAuth2 clients registered yet.</p>
            ) : (
              <div style={{ maxHeight: "22rem", overflowY: "auto", marginTop: "0.75rem" }}>
                {oauthClients.map((c) => (
                  <div className="param-row" key={c.id}>
                    <span className="path">{c.name}</span>
                    {c.revoked_at ? (
                      <span className="pill pill-danger">REVOKED</span>
                    ) : (
                      <button className="btn danger sm" onClick={() => revokeOAuthClient(c.id)}>
                        Revoke
                      </button>
                    )}
                    <span className="src mono">{c.client_id}</span>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}

      {tab === "health" && (
        <div className="split two-col">
          <div className="panel">
            <div className="form-row" style={{ marginTop: 0, justifyContent: "space-between" }}>
              <h3 style={{ margin: 0 }}>Adapter reachability</h3>
              <button className="btn" onClick={loadHealth} disabled={checkingHealth}>{checkingHealth ? "Checking…" : "Recheck"}</button>
            </div>
            {health && (
              <>
                <div className="param-row">
                  <span className="path">cmd/bssadapter</span>
                  <span className={health.reachable ? "pill pill-ok" : "pill pill-danger"}>{health.reachable ? "REACHABLE" : "UNREACHABLE"}</span>
                  <span className="src mono">{health.adapter_url} · {health.latency_ms}ms</span>
                </div>
                <div className="param-row">
                  <span className="path">Shared bearer token</span>
                  <span className={health.token_configured ? "pill pill-ok" : "pill pill-warn"}>{health.token_configured ? "CONFIGURED" : "NOT SET"}</span>
                </div>
                {health.error && <p className="dim" style={{ fontSize: "0.8rem", color: "var(--danger)" }}>{health.error}</p>}
              </>
            )}
          </div>

          <div className="panel">
            <h3>Account–device mappings by status</h3>
            {stats && Object.keys(stats.mappings_by_status).length > 0 ? (
              Object.entries(stats.mappings_by_status).map(([status, n]) => (
                <div className="param-row" key={status}>
                  <span className="path">{status}</span>
                  <span className="val">{n}</span>
                </div>
              ))
            ) : (
              <p className="dim" style={{ fontSize: "0.84rem" }}>No mappings recorded yet.</p>
            )}
          </div>

          <div className="panel">
            <h3>Orders by action</h3>
            {stats && Object.keys(stats.orders_by_action).length > 0 ? (
              <>
                {Object.entries(stats.orders_by_action).map(([action, n]) => (
                  <div className="param-row" key={action}>
                    <span className="path">{action}</span>
                    <span className="val">{n}</span>
                  </div>
                ))}
                <div className="param-row">
                  <span className="path">Last 24h</span>
                  <span className="val">{stats.orders_last_24h}</span>
                </div>
              </>
            ) : (
              <p className="dim" style={{ fontSize: "0.84rem" }}>No orders dispatched yet.</p>
            )}
          </div>

          <div className="panel">
            <h3>Webhook deliveries</h3>
            <div className="param-row">
              <span className="path">Subscriptions</span>
              <span className="val">{stats?.webhook_subscriptions ?? 0}</span>
            </div>
            {stats && Object.entries(stats.deliveries_by_status).map(([status, n]) => (
              <div className="param-row" key={status}>
                <span className="path">{status}</span>
                <span className="val">{n}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {tab === "troubleshoot" && (
        <div className="split two-col">
          <div className="panel">
            <h3>Auth enforcement check</h3>
            <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
              Calls the live adapter twice — once with the configured bearer token, once without. The second call should come back 401.
            </p>
            <button className="btn primary" onClick={runAuthCheck} disabled={running === "auth"}>
              {running === "auth" ? "Running…" : "Run check"}
            </button>
            {authResult && (
              <>
                <ResultCard result={authResult.with_token} />
                <ResultCard result={authResult.without_token} />
              </>
            )}
          </div>

          <div className="panel">
            <h3>Mapping lookup (Workflow A)</h3>
            <form onSubmit={runMappingLookup} className="form-row" style={{ flexWrap: "wrap" }}>
              <input placeholder="account_id" value={lookupAccountId} onChange={(e) => setLookupAccountId(e.target.value)} required />
              <button type="submit" className="btn primary" disabled={running === "lookup"}>{running === "lookup" ? "Running…" : "Run"}</button>
            </form>
            {lookupResult && <ResultCard result={lookupResult} />}
          </div>

          <div className="panel">
            <h3>Job status lookup (Workflow C)</h3>
            <form onSubmit={runJobStatus} className="form-row" style={{ flexWrap: "wrap" }}>
              <input placeholder="command_key" value={commandKey} onChange={(e) => setCommandKey(e.target.value)} required />
              <button type="submit" className="btn primary" disabled={running === "job"}>{running === "job" ? "Running…" : "Run"}</button>
            </form>
            {jobResult && <ResultCard result={jobResult} />}
          </div>

          <div className="panel">
            <h3>Order dispatch (Workflow B)</h3>
            <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem", color: "var(--warn)" }}>
              This is a real order — it queues a genuine MODIFY_WIFI job against the account's mapped device, not a dry run.
            </p>
            <form onSubmit={runOrderDispatch} className="form-row" style={{ flexWrap: "wrap" }}>
              <input placeholder="external_order_id" value={orderId} onChange={(e) => setOrderId(e.target.value)} required />
              <input placeholder="account_id" value={orderAccountId} onChange={(e) => setOrderAccountId(e.target.value)} required />
              <input placeholder="wifi_ssid" value={orderSsid} onChange={(e) => setOrderSsid(e.target.value)} required />
              <button type="submit" className="btn primary" disabled={running === "order"}>{running === "order" ? "Running…" : "Send order"}</button>
            </form>
            {orderResult && <ResultCard result={orderResult} />}
          </div>
        </div>
      )}
    </section>
  );
}
