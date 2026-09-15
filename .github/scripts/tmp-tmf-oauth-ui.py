from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    if text.count(old) != 1:
        raise SystemExit(f"{path}: expected exactly one match, found {text.count(old)}")
    p.write_text(text.replace(old, new, 1))

# Operator OpenAPI: creation request and returned client policy.
replace_once(
    "backend/openapi.yaml",
    '''      summary: Register a new OAuth2 client-credentials integration (superadmin) — client_secret returned exactly once
      requestBody:
        required: true
        content: { application/json: { schema: { type: object, required: [name], properties: { name: { type: string } } } } }
      responses:
        "201": { description: Created, content: { application/json: { schema: { $ref: "#/components/schemas/BSSOAuthClientCreateResult" } } } }
''',
    '''      summary: Register a new OAuth2 client-credentials integration with explicit TMF authorization policy (superadmin) — client_secret returned exactly once
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name, scopes]
              properties:
                name: { type: string }
                scopes:
                  type: array
                  items:
                    type: string
                    enum: [tmf:read, tmf:write, tmf:execute, tmf:acknowledge]
                  description: "TMF permissions granted to this integration. An empty list creates an authenticated but TMF-disabled client."
                account_ids:
                  type: array
                  items: { type: string }
                  description: "Accounts this client may access. Required for a non-empty scope list unless global_access is true; mutually exclusive with global_access."
                global_access:
                  type: boolean
                  default: false
                  description: "Deliberate fleet-wide TMF access. Mutually exclusive with account_ids."
      responses:
        "201": { description: Created, content: { application/json: { schema: { $ref: "#/components/schemas/BSSOAuthClientCreateResult" } } } }
        "400": { description: Invalid OAuth authorization policy }
''',
)
replace_once(
    "backend/openapi.yaml",
    '''    BSSOAuthClient:
      type: object
      properties:
        id: { type: string }
        name: { type: string }
        client_id: { type: string }
        created_at: { type: string, format: date-time }
        revoked_at: { type: string, format: date-time, nullable: true }
''',
    '''    BSSOAuthClient:
      type: object
      required: [id, name, client_id, scopes, account_ids, global_access, created_at]
      properties:
        id: { type: string }
        name: { type: string }
        client_id: { type: string }
        scopes:
          type: array
          items:
            type: string
            enum: [tmf:read, tmf:write, tmf:execute, tmf:acknowledge]
        account_ids: { type: array, items: { type: string } }
        global_access: { type: boolean }
        created_at: { type: string, format: date-time }
        revoked_at: { type: string, format: date-time, nullable: true }
''',
)

# Hand-maintained frontend types used by the BSS admin screen.
replace_once(
    "frontend/src/api/types.ts",
    '''export interface BSSOAuthClient {
  id: string;
  name: string;
  client_id: string;
  created_at: string;
  revoked_at?: string;
}
''',
    '''export interface BSSOAuthClient {
  id: string;
  name: string;
  client_id: string;
  scopes: string[];
  account_ids: string[];
  global_access: boolean;
  created_at: string;
  revoked_at?: string;
}
''',
)

# Client call must send the explicit policy instead of silently sending only a name.
replace_once(
    "frontend/src/api/client.ts",
    '''  createBSSOAuthClient: (name: string) =>
    request<BSSOAuthClientCreateResult>("/api/v1/bss/oauth-clients", { method: "POST", body: JSON.stringify({ name }) }),
''',
    '''  createBSSOAuthClient: (input: { name: string; scopes: string[]; account_ids: string[]; global_access: boolean }) =>
    request<BSSOAuthClientCreateResult>("/api/v1/bss/oauth-clients", { method: "POST", body: JSON.stringify(input) }),
''',
)

# BSS Integration screen: scope/account/global controls and policy visibility.
replace_once(
    "frontend/src/screens/BSSIntegration.tsx",
    '''type Tab = "setup" | "health" | "troubleshoot";
''',
    '''type Tab = "setup" | "health" | "troubleshoot";

const TMF_OAUTH_SCOPES = ["tmf:read", "tmf:write", "tmf:execute", "tmf:acknowledge"] as const;
''',
)
replace_once(
    "frontend/src/screens/BSSIntegration.tsx",
    '''  const [oauthClients, setOauthClients] = useState<BSSOAuthClient[]>([]);
  const [oauthClientName, setOauthClientName] = useState("");
  const [newClientSecret, setNewClientSecret] = useState<{ clientId: string; secret: string } | null>(null);
''',
    '''  const [oauthClients, setOauthClients] = useState<BSSOAuthClient[]>([]);
  const [oauthClientName, setOauthClientName] = useState("");
  const [oauthScopes, setOauthScopes] = useState<string[]>(["tmf:read"]);
  const [oauthAccountIds, setOauthAccountIds] = useState("");
  const [oauthGlobalAccess, setOauthGlobalAccess] = useState(false);
  const [newClientSecret, setNewClientSecret] = useState<{ clientId: string; secret: string } | null>(null);
''',
)
replace_once(
    "frontend/src/screens/BSSIntegration.tsx",
    '''  async function createOAuthClient(e: React.FormEvent) {
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
''',
    '''  async function createOAuthClient(e: React.FormEvent) {
    e.preventDefault();
    const accountIds = oauthGlobalAccess
      ? []
      : oauthAccountIds.split(",").map((value) => value.trim()).filter(Boolean);
    if (oauthScopes.length > 0 && !oauthGlobalAccess && accountIds.length === 0) {
      toast("Grant at least one account ID or deliberately enable fleet-wide access", "error");
      return;
    }
    try {
      const result = await api.createBSSOAuthClient({
        name: oauthClientName,
        scopes: oauthScopes,
        account_ids: accountIds,
        global_access: oauthGlobalAccess,
      });
      setOauthClientName("");
      setOauthScopes(["tmf:read"]);
      setOauthAccountIds("");
      setOauthGlobalAccess(false);
      setNewClientSecret({ clientId: result.client.client_id, secret: result.client_secret });
      toast("OAuth2 client registered", "success");
      await loadSetup();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to create OAuth2 client", "error");
    }
  }

  function toggleOAuthScope(scope: string) {
    setOauthScopes((current) => current.includes(scope) ? current.filter((value) => value !== scope) : [...current, scope]);
  }
''',
)
replace_once(
    "frontend/src/screens/BSSIntegration.tsx",
    '''            <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
              Recommended auth for new integrations (RFC 6749 client-credentials). The secret is shown once, right
              after creation — copy it now, it can't be retrieved again.
            </p>
            <form onSubmit={createOAuthClient} className="form-row" style={{ flexWrap: "wrap" }}>
              <input aria-label="Integration name, e.g. Salesforce Comm Cloud" placeholder="Integration name, e.g. Salesforce Comm Cloud" value={oauthClientName} onChange={(e) => setOauthClientName(e.target.value)} required />
              <button type="submit" className="btn primary">Register</button>
            </form>
''',
    '''            <p className="dim" style={{ marginTop: 0, fontSize: "0.82rem" }}>
              Recommended auth for new integrations (RFC 6749 client-credentials). Grant only the TMF permissions and
              accounts this integration needs, or deliberately choose fleet-wide access. The secret is shown once after creation.
            </p>
            <form onSubmit={createOAuthClient} style={{ marginTop: "0.75rem" }}>
              <div className="form-row" style={{ flexWrap: "wrap" }}>
                <input aria-label="Integration name, e.g. Salesforce Comm Cloud" placeholder="Integration name, e.g. Salesforce Comm Cloud" value={oauthClientName} onChange={(e) => setOauthClientName(e.target.value)} required />
                <input
                  aria-label="Authorized account IDs"
                  placeholder="account IDs, comma-separated"
                  value={oauthAccountIds}
                  onChange={(e) => setOauthAccountIds(e.target.value)}
                  disabled={oauthGlobalAccess}
                />
                <label style={{ display: "inline-flex", alignItems: "center", gap: "0.35rem" }}>
                  <input
                    type="checkbox"
                    checked={oauthGlobalAccess}
                    onChange={(e) => {
                      setOauthGlobalAccess(e.target.checked);
                      if (e.target.checked) setOauthAccountIds("");
                    }}
                  />
                  Fleet-wide access
                </label>
              </div>
              <div className="form-row" role="group" aria-label="TMF OAuth scopes" style={{ flexWrap: "wrap", marginTop: "0.5rem" }}>
                {TMF_OAUTH_SCOPES.map((scope) => (
                  <label key={scope} style={{ display: "inline-flex", alignItems: "center", gap: "0.35rem" }}>
                    <input type="checkbox" checked={oauthScopes.includes(scope)} onChange={() => toggleOAuthScope(scope)} />
                    <code>{scope}</code>
                  </label>
                ))}
                <button type="submit" className="btn primary">Register</button>
              </div>
              <p className="dim" style={{ margin: "0.45rem 0 0", fontSize: "0.76rem" }}>
                A client with no scopes may authenticate but cannot call TMF routes. Any scoped client must be account-bound or explicitly fleet-wide.
              </p>
            </form>
''',
)
replace_once(
    "frontend/src/screens/BSSIntegration.tsx",
    '''                    <span className="src mono">{c.client_id}</span>
''',
    '''                    <span className="src mono">{c.client_id}</span>
                    <span className="src">
                      {c.scopes.length > 0 ? c.scopes.join(", ") : "no TMF scopes"} · {c.global_access ? "fleet-wide" : c.account_ids.length > 0 ? c.account_ids.join(", ") : "no accounts"}
                    </span>
''',
)
