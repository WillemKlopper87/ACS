// Thin typed wrapper over cmd/api's REST surface. No caching/retry layer —
// each screen calls these directly and manages its own loading state; add
// a real data layer (React Query, etc.) if/when the screen count outgrows
// hand-rolled useEffect fetching.
import type {
  ApplyTemplateResult,
  AuditEntry,
  BSSAdapterCallResult,
  BSSHealth,
  BSSMapping,
  BSSStats,
  BSSOAuthClient,
  BSSOAuthClientCreateResult,
  BSSWebhookSubscription,
  BulkActionResponse,
  CLICredential,
  VPNConcentrator,
  VPNEnrollResult,
  VPNPeer,
  ConfigTemplate,
  WebGUIConfig,
  ConnectionRequestResponse,
  Credential,
  Device,
  DeviceGroup,
  DeviceGroupCount,
  FirmwareImage,
  FleetHealth,
  Job,
  Operator,
  OperatorScope,
  Permission,
  Role,
  RolePermissionsMatrix,
  Region,
  Customer,
  Project,
  ImportResult,
  DashboardData,
  DashboardWidget,
  ParameterCache,
  ParameterHistoryEntry,
  Policy,
  QueueResponse,
  RefreshCellularResponse,
  Rollout,
  RolloutDetail,
  ScheduledJob,
  TemplateParameter,
  UploadedFile,
} from "./types";
import { getAuthState, clearAuth, markAuthRequired } from "../auth/tokenStore";

const BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8080";

class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

const LOGIN_PATH = "/api/v1/auth/login";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const { token } = getAuthState();
  // A FormData body (firmware upload) needs the browser to set its own
  // multipart boundary in Content-Type — defaulting to application/json
  // here would break that upload silently.
  const isFormData = init?.body instanceof FormData;
  const headers: Record<string, string> = {
    ...(isFormData ? {} : { "Content-Type": "application/json" }),
    ...(init?.headers as Record<string, string> | undefined),
  };
  if (token) headers["Authorization"] = `Bearer ${token}`;

  const res = await fetch(`${BASE_URL}${path}`, { ...init, headers });

  // A 401 on anything other than the login call itself means either no
  // token was ever presented, or the one we have expired — either way,
  // the backend has operator auth enabled (build plan §4 Phase 6) and
  // wants a fresh login. Login's own 401 (wrong password) is a normal
  // rejected-credentials error, not a "you need to log in" signal.
  if (res.status === 401 && path !== LOGIN_PATH) {
    clearAuth();
    markAuthRequired();
  }

  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new ApiError(res.status, body || res.statusText);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
  login: (username: string, password: string) =>
    request<{ token: string; role: string; expires_at: string }>(LOGIN_PATH, {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),

  listDevices: (page = 1, pageSize = 100) =>
    request<{ items: Device[]; total: number }>(`/api/v1/devices?page=${page}&page_size=${pageSize}`),

  getDevicesSummary: () => request<{ groups: DeviceGroupCount[] }>("/api/v1/devices/summary"),

  matchingDeviceIds: (filters: { manufacturer?: string; online_status?: string; connection_request_mode?: string; search?: string }) => {
    const params = new URLSearchParams();
    if (filters.manufacturer) params.set("manufacturer", filters.manufacturer);
    if (filters.online_status) params.set("online_status", filters.online_status);
    if (filters.connection_request_mode) params.set("connection_request_mode", filters.connection_request_mode);
    if (filters.search) params.set("search", filters.search);
    return request<{ ids: string[]; count: number }>(`/api/v1/devices/ids?${params.toString()}`);
  },

  getFleetHealth: () => request<FleetHealth>("/api/v1/fleet-health"),

  bulkAction: (
    deviceIds: string[],
    action: "SET_PARAMETER" | "CONNECTION_REQUEST" | "REFRESH_CELLULAR",
    opts?: { parameters?: { name: string; value: string; type?: string }[]; timeoutSeconds?: number },
  ) =>
    request<BulkActionResponse>("/api/v1/devices/bulk-actions", {
      method: "POST",
      body: JSON.stringify({
        device_ids: deviceIds,
        action,
        parameters: opts?.parameters,
        timeout_seconds: opts?.timeoutSeconds,
      }),
    }),

  getDevice: (id: string) => request<Device>(`/api/v1/devices/${id}`),

  updateDeviceTags: (id: string, tags: string[]) =>
    request<{ device_id: string; tags: string[] }>(`/api/v1/devices/${id}/tags`, { method: "PUT", body: JSON.stringify({ tags }) }),

  getDeviceParameters: (id: string) =>
    request<{ parameters: ParameterCache }>(`/api/v1/devices/${id}/parameters`),

  putDeviceParameters: (
    id: string,
    parameters: { name: string; value: string; type?: string }[],
  ) =>
    request<QueueResponse>(`/api/v1/devices/${id}/parameters`, {
      method: "PUT",
      body: JSON.stringify({ parameters }),
    }),

  createConnectionRequest: (id: string, timeoutSeconds = 30) =>
    request<ConnectionRequestResponse>(`/api/v1/devices/${id}/connection-request`, {
      method: "POST",
      body: JSON.stringify({ timeout_seconds: timeoutSeconds }),
    }),

  refreshCellularDiagnostics: (id: string) =>
    request<RefreshCellularResponse>(`/api/v1/devices/${id}/parameters/refresh-cellular`, {
      method: "POST",
    }),

  refreshWifiClients: (id: string) =>
    request<QueueResponse & { parameters: string[] }>(`/api/v1/devices/${id}/parameters/refresh-wifi-clients`, {
      method: "POST",
    }),

  createDiagnosticsPing: (id: string, host: string) =>
    request<QueueResponse>(`/api/v1/devices/${id}/diagnostics/ping`, {
      method: "POST",
      body: JSON.stringify({ host }),
    }),

  createDiagnosticsTraceroute: (id: string, host: string) =>
    request<QueueResponse>(`/api/v1/devices/${id}/diagnostics/traceroute`, {
      method: "POST",
      body: JSON.stringify({ host }),
    }),

  getParameterHistory: (id: string, name: string) =>
    request<{ name: string; items: ParameterHistoryEntry[] }>(`/api/v1/devices/${id}/parameters/history?name=${encodeURIComponent(name)}`),

  // Live GET_PARAMETER trigger — queues a real read against the device
  // (distinct from getDeviceParameters, which only reads the cache).
  getParametersLive: (id: string, paths: string[]) =>
    request<QueueResponse>(`/api/v1/devices/${id}/parameters/get`, {
      method: "POST",
      body: JSON.stringify({ paths }),
    }),

  discoverParameters: (id: string) =>
    request<QueueResponse>(`/api/v1/devices/${id}/discover-parameters`, { method: "POST" }),

  getParameterNames: (id: string) =>
    request<{ names: Record<string, boolean>; discovered_at: string | null }>(`/api/v1/devices/${id}/parameter-names`),

  scheduleInform: (id: string, delaySeconds: number) =>
    request<QueueResponse>(`/api/v1/devices/${id}/schedule-inform`, {
      method: "POST",
      body: JSON.stringify({ delay_seconds: delaySeconds }),
    }),

  setParameterAttributes: (id: string, attributes: { name: string; notification: number }[]) =>
    request<QueueResponse>(`/api/v1/devices/${id}/parameter-attributes`, {
      method: "POST",
      body: JSON.stringify({ attributes }),
    }),

  getParameterAttributes: (id: string, paths: string[]) =>
    request<QueueResponse>(`/api/v1/devices/${id}/parameter-attributes/get`, {
      method: "POST",
      body: JSON.stringify({ paths }),
    }),

  createUpload: (id: string, fileType: string) =>
    request<QueueResponse & { upload_id: string }>(`/api/v1/devices/${id}/uploads`, {
      method: "POST",
      body: JSON.stringify({ file_type: fileType }),
    }),
  listDeviceUploads: (id: string) => request<{ items: UploadedFile[] }>(`/api/v1/devices/${id}/uploads`),

  addObject: (id: string, objectPath: string) =>
    request<QueueResponse>(`/api/v1/devices/${id}/objects`, {
      method: "POST",
      body: JSON.stringify({ object_path: objectPath }),
    }),

  deleteObject: (id: string, objectPath: string) =>
    request<QueueResponse>(`/api/v1/devices/${id}/objects/delete`, {
      method: "POST",
      body: JSON.stringify({ object_path: objectPath }),
    }),

  rebootDevice: (id: string) =>
    request<QueueResponse>(`/api/v1/devices/${id}/reboot`, { method: "POST" }),

  factoryResetDevice: (id: string) =>
    request<QueueResponse>(`/api/v1/devices/${id}/factory-reset`, { method: "POST" }),

  listJobs: () => request<{ items: Job[]; total: number }>("/api/v1/jobs"),

  listDeviceJobs: (id: string) =>
    request<{ items: Job[]; total: number }>(`/api/v1/devices/${id}/jobs`),

  getJob: (commandKey: string) => request<Job>(`/api/v1/jobs/${commandKey}`),

  // --- device groups (build plan §4 Phase 7) ---
  listDeviceGroups: () => request<{ items: DeviceGroup[] }>("/api/v1/device-groups"),
  getDeviceGroup: (id: string) => request<DeviceGroup>(`/api/v1/device-groups/${id}`),
  createDeviceGroup: (name: string, description?: string) =>
    request<DeviceGroup>("/api/v1/device-groups", { method: "POST", body: JSON.stringify({ name, description }) }),
  deleteDeviceGroup: (id: string) => request<void>(`/api/v1/device-groups/${id}`, { method: "DELETE" }),
  addDeviceGroupMembers: (id: string, deviceIds: string[]) =>
    request<DeviceGroup>(`/api/v1/device-groups/${id}/members`, { method: "POST", body: JSON.stringify({ device_ids: deviceIds }) }),
  removeDeviceGroupMember: (id: string, deviceId: string) =>
    request<void>(`/api/v1/device-groups/${id}/members/${deviceId}`, { method: "DELETE" }),

  // --- scheduled jobs (build plan §4 Phase 7) ---
  listScheduledJobs: () => request<{ items: ScheduledJob[] }>("/api/v1/scheduled-jobs"),
  createScheduledJob: (input: {
    name: string;
    job_type: string;
    target_type: "DEVICE" | "GROUP";
    target_id: string;
    payload: Record<string, unknown>;
    interval_seconds: number;
  }) => request<ScheduledJob>("/api/v1/scheduled-jobs", { method: "POST", body: JSON.stringify(input) }),
  deleteScheduledJob: (id: string) => request<void>(`/api/v1/scheduled-jobs/${id}`, { method: "DELETE" }),
  setScheduledJobEnabled: (id: string, enabled: boolean) =>
    request<ScheduledJob>(`/api/v1/scheduled-jobs/${id}/${enabled ? "enable" : "disable"}`, { method: "POST" }),

  // --- policies (build plan §4 Phase 7) ---
  listPolicies: () => request<{ items: Policy[] }>("/api/v1/policies"),
  createPolicy: (input: { name: string; model_filter?: string; parameter_name: string; desired_value: string }) =>
    request<Policy>("/api/v1/policies", { method: "POST", body: JSON.stringify(input) }),
  deletePolicy: (id: string) => request<void>(`/api/v1/policies/${id}`, { method: "DELETE" }),
  setPolicyEnabled: (id: string, enabled: boolean) =>
    request<Policy>(`/api/v1/policies/${id}/${enabled ? "enable" : "disable"}`, { method: "POST" }),

  // --- config templates (nice-to-have feature backlog) ---
  listTemplates: () => request<{ items: ConfigTemplate[] }>("/api/v1/config-templates"),
  createTemplate: (input: {
    name: string;
    description?: string;
    parameters: TemplateParameter[];
    model_filter?: string;
    auto_apply?: boolean;
  }) => request<ConfigTemplate>("/api/v1/config-templates", { method: "POST", body: JSON.stringify(input) }),
  deleteTemplate: (id: string) => request<void>(`/api/v1/config-templates/${id}`, { method: "DELETE" }),
  applyTemplate: (id: string, target: { device_ids?: string[]; group_id?: string }) =>
    request<ApplyTemplateResult>(`/api/v1/config-templates/${id}/apply`, { method: "POST", body: JSON.stringify(target) }),

  // --- firmware rollouts (build plan §4 Phase 7 / design doc v3 §9.5) ---
  listFirmwareImages: () => request<{ items: FirmwareImage[] }>("/api/v1/firmware/images"),
  uploadFirmwareImage: (file: File, vendor: string, model: string, version: string, channel: string) => {
    const form = new FormData();
    form.append("file", file);
    form.append("vendor", vendor);
    form.append("model", model);
    form.append("version", version);
    form.append("channel", channel);
    return request<FirmwareImage>("/api/v1/firmware/images", { method: "POST", body: form });
  },
  listRollouts: () => request<{ items: Rollout[] }>("/api/v1/firmware/rollouts"),
  getRollout: (id: string) => request<RolloutDetail>(`/api/v1/firmware/rollouts/${id}`),
  createRollout: (input: {
    name: string;
    firmware_image_id: string;
    rollback_firmware_image_id?: string;
    model_filter?: string;
    current_version_filter?: string;
    canary_percentage?: number;
    maximum_failure_rate?: number;
  }) => request<Rollout>("/api/v1/firmware/rollouts", { method: "POST", body: JSON.stringify(input) }),
  startRollout: (id: string) => request<{ dispatched: number; batch_size: number; status: string }>(`/api/v1/firmware/rollouts/${id}/start`, { method: "POST" }),
  advanceRollout: (id: string) => request<{ dispatched: number; batch_size: number; status: string; final_wave: boolean }>(`/api/v1/firmware/rollouts/${id}/advance`, { method: "POST" }),

  // --- audit log (design doc v3 §11.8) ---
  listAuditLog: (filters?: { device_id?: string; action?: string }) => {
    const params = new URLSearchParams();
    if (filters?.device_id) params.set("device_id", filters.device_id);
    if (filters?.action) params.set("action", filters.action);
    const qs = params.toString();
    return request<{ items: AuditEntry[] }>(`/api/v1/audit-log${qs ? `?${qs}` : ""}`);
  },

  // --- device credentials (build plan §4 Phase 6 / design doc v3 §11.6) ---
  listDeviceCredentials: (deviceId: string) => request<{ items: Credential[] }>(`/api/v1/devices/${deviceId}/credentials`),
  rotateDeviceCredential: (deviceId: string) => request<Credential>(`/api/v1/devices/${deviceId}/credentials/rotate`, { method: "POST" }),
  activateDeviceCredential: (deviceId: string, credentialId: string) =>
    request<Credential>(`/api/v1/devices/${deviceId}/credentials/${credentialId}/activate`, { method: "POST" }),
  revokeDeviceCredential: (deviceId: string, credentialId: string) =>
    request<Credential>(`/api/v1/devices/${deviceId}/credentials/${credentialId}/revoke`, { method: "POST" }),

  // --- operator management (superadmin-only) ---
  listOperators: () => request<{ items: Operator[] }>("/api/v1/auth/operators"),
  createOperator: (username: string, password: string, role: Role, email?: string) =>
    request<Operator>("/api/v1/auth/operators", { method: "POST", body: JSON.stringify({ username, password, role, email }) }),
  resetOperatorPassword: (operatorId: string, newPassword: string) =>
    request<void>(`/api/v1/auth/operators/${operatorId}/password`, { method: "PUT", body: JSON.stringify({ new_password: newPassword }) }),

  // --- RBAC permission matrix (admin-platform backlog) ---
  getRolePermissions: () => request<RolePermissionsMatrix>("/api/v1/auth/role-permissions"),
  setRolePermission: (role: Role, permission: Permission, granted: boolean) =>
    request<void>("/api/v1/auth/role-permissions", { method: "PUT", body: JSON.stringify({ role, permission, granted }) }),

  // --- password reset (public — no operator JWT required) ---
  requestPasswordReset: (username: string) =>
    request<void>("/api/v1/auth/password-reset/request", { method: "POST", body: JSON.stringify({ username }) }),
  confirmPasswordReset: (token: string, newPassword: string) =>
    request<void>("/api/v1/auth/password-reset/confirm", { method: "POST", body: JSON.stringify({ token, new_password: newPassword }) }),

  // --- multi-tenancy (admin-platform backlog) ---
  listRegions: () => request<{ items: Region[] }>("/api/v1/regions"),
  createRegion: (name: string) => request<Region>("/api/v1/regions", { method: "POST", body: JSON.stringify({ name }) }),
  deleteRegion: (id: string) => request<void>(`/api/v1/regions/${id}`, { method: "DELETE" }),

  listCustomers: () => request<{ items: Customer[] }>("/api/v1/customers"),
  createCustomer: (name: string, regionId?: string) =>
    request<Customer>("/api/v1/customers", { method: "POST", body: JSON.stringify({ name, region_id: regionId }) }),
  deleteCustomer: (id: string) => request<void>(`/api/v1/customers/${id}`, { method: "DELETE" }),

  listProjects: () => request<{ items: Project[] }>("/api/v1/projects"),
  createProject: (name: string, description?: string) =>
    request<Project>("/api/v1/projects", { method: "POST", body: JSON.stringify({ name, description }) }),
  deleteProject: (id: string) => request<void>(`/api/v1/projects/${id}`, { method: "DELETE" }),

  assignDeviceCustomer: (deviceId: string, customerId: string | null) =>
    request<void>(`/api/v1/devices/${deviceId}/customer`, { method: "PUT", body: JSON.stringify({ customer_id: customerId }) }),
  getDeviceProjects: (deviceId: string) => request<{ items: Project[] }>(`/api/v1/devices/${deviceId}/projects`),
  setDeviceProjects: (deviceId: string, projectIds: string[]) =>
    request<void>(`/api/v1/devices/${deviceId}/projects`, { method: "PUT", body: JSON.stringify({ project_ids: projectIds }) }),

  getOperatorScopes: (operatorId: string) => request<{ items: OperatorScope[] }>(`/api/v1/auth/operators/${operatorId}/scopes`),
  setOperatorScopes: (operatorId: string, scopes: OperatorScope[]) =>
    request<void>(`/api/v1/auth/operators/${operatorId}/scopes`, { method: "POST", body: JSON.stringify({ scopes }) }),

  // --- Excel reporting (admin-platform backlog) ---
  updateDeviceLocation: (deviceId: string, location: string) =>
    request<{ device_id: string; location: string }>(`/api/v1/devices/${deviceId}/location`, {
      method: "PUT",
      body: JSON.stringify({ location }),
    }),
  // Not the generic request() helper — this response is a binary .xlsx
  // file, not JSON, and needs its own Blob-download handling rather than
  // res.json(). Auth still goes through the same Bearer header since this
  // is a real fetch(), not a plain <a href> the browser can't attach a
  // token to.
  exportDevicesReport: async (filters: { customer_id?: string; region_id?: string; project_id?: string }) => {
    const { token } = getAuthState();
    const qs = new URLSearchParams(Object.entries(filters).filter(([, v]) => v) as [string, string][]);
    const res = await fetch(`${BASE_URL}/api/v1/reports/devices/export?${qs.toString()}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!res.ok) {
      const body = await res.text().catch(() => "");
      throw new ApiError(res.status, body || res.statusText);
    }
    const disposition = res.headers.get("Content-Disposition") ?? "";
    const match = disposition.match(/filename="([^"]+)"/);
    const filename = match?.[1] ?? "acs-devices.xlsx";
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  },

  // --- customizable dashboard (admin-platform backlog) ---
  getDashboard: () => request<DashboardData>("/api/v1/dashboard"),
  getDashboardLayout: () => request<{ widgets: DashboardWidget[] }>("/api/v1/dashboard/layout"),
  setDashboardLayout: (widgets: DashboardWidget[]) =>
    request<void>("/api/v1/dashboard/layout", { method: "PUT", body: JSON.stringify({ widgets }) }),

  importDevices: (format: "json" | "csv" | "xml", body: string) =>
    request<ImportResult>(`/api/v1/devices/import?format=${format}`, {
      method: "POST",
      headers: { "Content-Type": format === "json" ? "application/json" : "text/plain" },
      body,
    }),

  // --- device CLI (SSH/Telnet) console — admin-platform backlog, scaffolded
  // "build now, functional later": see internal/cliaccess's doc comment for
  // the CGNAT reachability caveat this carries until a VPN/tunnel exists.
  listCLICredentials: (deviceId: string) => request<{ items: CLICredential[] }>(`/api/v1/devices/${deviceId}/cli/credentials`),
  createCLICredential: (
    deviceId: string,
    input: { protocol: "SSH" | "TELNET"; host: string; port: number; username: string; password: string },
  ) => request<CLICredential>(`/api/v1/devices/${deviceId}/cli/credentials`, { method: "POST", body: JSON.stringify(input) }),
  deleteCLICredential: (deviceId: string, credentialId: string) =>
    request<void>(`/api/v1/devices/${deviceId}/cli/credentials/${credentialId}`, { method: "DELETE" }),

  // Browser ticket (audit P1.4): the session JWT never goes in a URL.
  // The two handshakes a browser can't attach a header to (WebSocket,
  // iframe) get a 60-second, audience-bound ticket minted just before
  // use; the backend accepts nothing else in ?token=. Returns null when
  // auth is disabled (dev mode) so the URL builders omit the param.
  browserTicket: async (): Promise<string | null> => {
    if (!getAuthState().token) return null;
    const { ticket } = await request<{ ticket: string }>("/api/v1/auth/ticket", { method: "POST" });
    return ticket;
  },

  // Not a fetch() call — the caller opens this URL directly as a WebSocket.
  // Async because it first mints a browser ticket for the handshake.
  cliConnectURL: async (deviceId: string, credentialId: string) => {
    const wsBase = BASE_URL.replace(/^http/, "ws");
    const ticket = await api.browserTicket();
    const qs = new URLSearchParams({ credential_id: credentialId, ...(ticket ? { token: ticket } : {}) });
    return `${wsBase}/api/v1/devices/${deviceId}/cli/connect?${qs.toString()}`;
  },

  // --- device web-GUI embed (admin-platform backlog, same scaffold-now
  // caveat as the CLI console) ---
  getWebGUIConfig: (deviceId: string) => request<WebGUIConfig>(`/api/v1/devices/${deviceId}/webgui`),
  setWebGUIConfig: (deviceId: string, input: { base_url: string; username?: string; password?: string }) =>
    request<WebGUIConfig>(`/api/v1/devices/${deviceId}/webgui`, { method: "PUT", body: JSON.stringify(input) }),
  deleteWebGUIConfig: (deviceId: string) => request<void>(`/api/v1/devices/${deviceId}/webgui`, { method: "DELETE" }),
  // Iframe src — same ticket handshake as the WS URL above.
  webGUIProxyURL: async (deviceId: string) => {
    const ticket = await api.browserTicket();
    const qs = ticket ? `?token=${encodeURIComponent(ticket)}` : "";
    return `${BASE_URL}/api/v1/devices/${deviceId}/webgui/proxy/${qs}`;
  },

  // --- BSS integration admin panel (admin-platform backlog) ---
  listBSSMappings: () => request<{ items: BSSMapping[] }>("/api/v1/bss/mappings"),
  createBSSMapping: (accountId: string, ouiSerial: string, servicePlan?: string) =>
    request<BSSMapping>("/api/v1/bss/mappings", {
      method: "POST",
      body: JSON.stringify({ account_id: accountId, oui_serial: ouiSerial, service_plan: servicePlan }),
    }),
  listBSSOAuthClients: () => request<{ items: BSSOAuthClient[] }>("/api/v1/bss/oauth-clients"),
  createBSSOAuthClient: (name: string) =>
    request<BSSOAuthClientCreateResult>("/api/v1/bss/oauth-clients", { method: "POST", body: JSON.stringify({ name }) }),
  revokeBSSOAuthClient: (id: string) => request<void>(`/api/v1/bss/oauth-clients/${id}`, { method: "DELETE" }),
  listBSSWebhooks: () => request<{ items: BSSWebhookSubscription[] }>("/api/v1/bss/webhooks"),
  createBSSWebhook: (input: { account_id?: string; target_url: string; secret: string; event_types: string[] }) =>
    request<BSSWebhookSubscription>("/api/v1/bss/webhooks", { method: "POST", body: JSON.stringify(input) }),
  deleteBSSWebhook: (id: string) => request<void>(`/api/v1/bss/webhooks/${id}`, { method: "DELETE" }),
  getBSSStats: () => request<BSSStats>("/api/v1/bss/stats"),
  getBSSHealth: () => request<BSSHealth>("/api/v1/bss/health"),
  troubleshootMappingLookup: (accountId: string) =>
    request<BSSAdapterCallResult>("/api/v1/bss/troubleshoot/mapping-lookup", { method: "POST", body: JSON.stringify({ account_id: accountId }) }),
  troubleshootAuthCheck: () =>
    request<{ with_token: BSSAdapterCallResult; without_token: BSSAdapterCallResult }>("/api/v1/bss/troubleshoot/auth-check", { method: "POST" }),
  troubleshootJobStatus: (commandKey: string) =>
    request<BSSAdapterCallResult>("/api/v1/bss/troubleshoot/job-status", { method: "POST", body: JSON.stringify({ command_key: commandKey }) }),
  troubleshootOrderDispatch: (input: { external_order_id: string; account_id: string; wifi_ssid?: string; wifi_password?: string }) =>
    request<BSSAdapterCallResult>("/api/v1/bss/troubleshoot/order-dispatch", { method: "POST", body: JSON.stringify(input) }),

  // --- VPN/tunnel concentrator (admin-platform backlog, deliberately last) ---
  enrollVPNPeer: (deviceId: string) => request<VPNEnrollResult>(`/api/v1/devices/${deviceId}/vpn/enroll`, { method: "POST" }),
  getVPNPeerConfig: (deviceId: string) => request<VPNEnrollResult>(`/api/v1/devices/${deviceId}/vpn/config`),
  listVPNPeers: () => request<{ items: VPNPeer[] }>("/api/v1/vpn/peers"),
  revokeVPNPeer: (peerId: string) => request<void>(`/api/v1/vpn/peers/${peerId}`, { method: "DELETE" }),
  getVPNConcentrator: () => request<VPNConcentrator>("/api/v1/vpn/concentrator"),
};

export { ApiError };
