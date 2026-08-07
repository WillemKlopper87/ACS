// Mirrors the JSON shapes cmd/api actually returns (cmd/api/main.go
// deviceResponse / jobResponse / cachedValueResponse) — kept in sync by
// hand rather than generated, since the backend has no OpenAPI spec yet.

export interface Device {
  id: string;
  oui_serial: string;
  manufacturer: string;
  oui: string;
  product_class: string;
  serial_number: string;
  data_model_root: string;
  online_status: string;
  last_inform_at?: string;
  last_inform_event_codes?: string[];
  connection_request_url?: string;
  connection_request_mode: string;
  last_connection_request_at?: string;
  last_connection_request_status?: string;
  tags?: string[];
  cwmp_auth_mode?: string;
  udp_connection_request_address?: string;
  nat_detected?: boolean;
  customer_id?: string;
  location?: string;
}

export interface Job {
  command_key: string;
  device_id: string;
  type: string;
  status: string;
  created_at: string;
  updated_at: string;
  completed_at?: string;
  fault_code?: string;
  fault_string?: string;
  result_detail?: { instance_number?: number; [key: string]: unknown };
}

export interface CachedParameter {
  value: string;
  type?: string;
  updated_at: string;
  source: string;
}

export type ParameterCache = Record<string, CachedParameter>;

export interface CLICredential {
  id: string;
  device_id: string;
  protocol: "SSH" | "TELNET";
  host: string;
  port: number;
  username: string;
  created_at: string;
}

export interface WebGUIConfig {
  configured?: false;
  device_id?: string;
  base_url?: string;
  username?: string;
  updated_at?: string;
}

export interface QueueResponse {
  command_key: string;
  status: string;
}

export interface ConnectionRequestResponse extends QueueResponse {
  reachability: {
    connection_request_mode: string;
    recommendation: string;
  };
}

export interface RefreshCellularResponse extends QueueResponse {
  matched_vendor: string;
  parameters: string[];
}

export interface DeviceGroupCount {
  Manufacturer: string;
  OnlineStatus: string;
  ConnectionRequestMode: string;
  Count: number;
}

export interface BulkActionResult {
  device_id: string;
  command_key?: string;
  error?: string;
}

export interface BulkActionResponse {
  action: string;
  requested: number;
  succeeded: number;
  results: BulkActionResult[];
}

export interface DeviceGroup {
  id: string;
  name: string;
  description?: string;
  member_count: number;
  created_at: string;
  device_ids?: string[];
}

export interface ScheduledJob {
  id: string;
  name: string;
  job_type: string;
  target_type: "DEVICE" | "GROUP";
  target_id: string;
  payload: Record<string, unknown>;
  interval_seconds: number;
  enabled: boolean;
  next_run_at: string;
  last_run_at?: string;
  created_at: string;
}

export interface Policy {
  id: string;
  name: string;
  model_filter?: string;
  parameter_name: string;
  desired_value: string;
  enabled: boolean;
  created_at: string;
}

export interface Rollout {
  id: string;
  name: string;
  firmware_image_id: string;
  rollback_firmware_image_id?: string;
  model_filter?: string;
  current_version_filter?: string;
  canary_percentage: number;
  maximum_failure_rate: number;
  status: string;
  created_at: string;
  eligible_devices?: number;
  rollback_dispatched_at?: string;
}

export interface RolloutDevice {
  device_id: string;
  oui_serial: string;
  state: string;
  command_key?: string;
}

export interface AuditEntry {
  id: string;
  occurred_at: string;
  actor: string;
  device_id?: string;
  action: string;
  details?: Record<string, unknown>;
}

export interface FirmwareImage {
  id: string;
  vendor: string;
  model: string;
  version: string;
  channel: string;
  filename: string;
  file_size_bytes: number;
  created_at: string;
}

export interface Credential {
  id: string;
  version: number;
  username: string;
  status: "PENDING" | "ACTIVE" | "GRACE" | "REVOKED";
  command_key?: string;
  created_at: string;
  activated_at?: string;
  revoked_at?: string;
}

export interface RolloutDetail extends Rollout {
  devices: RolloutDevice[];
  state_counts: Record<string, number>;
  failure_rate: number;
  terminal_count: number;
}

export type Role = "readonly" | "noc" | "manager" | "superadmin";

export interface Operator {
  id: string;
  username: string;
  email?: string;
  role: Role;
  created_at: string;
}

export type Permission =
  | "devices.write"
  | "connection_request"
  | "diagnostics.run"
  | "firmware.manage"
  | "template.manage"
  | "policy.manage"
  | "schedule.manage"
  | "group.manage"
  | "credential.manage"
  | "cli.access"
  | "bulk_actions"
  | "upload.request";

export interface RolePermissionsMatrix {
  roles: Role[];
  permissions: Permission[];
  matrix: Record<string, Record<string, boolean>>;
}

// --- multi-tenancy (admin-platform backlog) ---
export interface Region {
  id: string;
  name: string;
  created_at: string;
}

export interface Customer {
  id: string;
  name: string;
  region_id?: string;
}

export interface Project {
  id: string;
  name: string;
  description?: string;
}

export interface OperatorScope {
  type: "region" | "customer";
  id: string;
}

// --- customizable dashboard (admin-platform backlog) ---
export interface DashboardAlarm {
  severity: "critical" | "warning";
  title: string;
  detail: string;
}

export interface TemperatureReading {
  DeviceID: string;
  ParameterName: string;
  Value: string;
}

export interface DashboardData {
  devices_by_status: Record<string, number>;
  devices_by_reachability: Record<string, number>;
  inform_recency: Record<string, number>;
  group_by: {
    customer: Record<string, number>;
    region: Record<string, number>;
    project: Record<string, number>;
    manufacturer: Record<string, number>;
  };
  jobs_last_24h_total: number;
  job_success_rate_pct: number;
  firmware: { up_to_date: number; outdated: number; unknown: number };
  alarms: DashboardAlarm[];
  temperature: TemperatureReading[] | null;
  scoped: boolean;
  generated_at: string;
}

export interface DashboardWidget {
  id: string;
  enabled: boolean;
}

export interface ImportResult {
  total_rows: number;
  succeeded: number;
  failed: number;
  results: { serial_number: string; status: string; error?: string }[];
}

export interface FleetHealth {
  devices_by_status: Record<string, number>;
  devices_by_reachability: Record<string, number>;
  inform_recency: Record<string, number>;
  jobs_last_24h: Record<string, number>;
  jobs_last_24h_total: number;
  job_success_rate_pct: number;
  generated_at: string;
}

export interface TemplateParameter {
  name: string;
  value: string;
  type: string;
}

export interface ConfigTemplate {
  id: string;
  name: string;
  description?: string;
  parameters: TemplateParameter[];
  model_filter?: string;
  auto_apply: boolean;
  created_at: string;
}

export interface ApplyTemplateResult {
  template_id: string;
  requested: number;
  succeeded: number;
  results: { device_id: string; command_key?: string; error?: string }[];
}

export interface UploadedFile {
  id: string;
  device_id: string;
  file_type: string;
  status: "PENDING" | "RECEIVED";
  filename?: string;
  file_size_bytes?: number;
  sha256?: string;
  created_at: string;
  received_at?: string;
}

export interface ParameterHistoryEntry {
  value: string;
  type?: string;
  source: string;
  recorded_at: string;
}

// --- BSS integration admin panel (admin-platform backlog) ---
export interface BSSMapping {
  id: string;
  account_id: string;
  device_id: string;
  oui_serial: string;
  service_plan?: string;
  status: "PENDING_ACTIVE" | "ACTIVE" | "SUSPENDED" | "TERMINATED";
}

export interface BSSWebhookSubscription {
  id: string;
  account_id: string | null;
  target_url: string;
  secret: string;
  event_types: string[];
  created_at: string;
}

export interface BSSStats {
  mappings_by_status: Record<string, number>;
  orders_by_action: Record<string, number>;
  orders_last_24h: number;
  webhook_subscriptions: number;
  deliveries_by_status: Record<string, number>;
}

export interface BSSHealth {
  adapter_url: string;
  reachable: boolean;
  latency_ms: number;
  token_configured: boolean;
  error?: string;
}

export interface BSSAdapterCallResult {
  description: string;
  method: string;
  url: string;
  status_code: number;
  latency_ms: number;
  body: string;
  error?: string;
}

// --- BSS OAuth2 client-credentials (production auth, admin-platform backlog) ---
export interface BSSOAuthClient {
  id: string;
  name: string;
  client_id: string;
  created_at: string;
  revoked_at?: string;
}

export interface BSSOAuthClientCreateResult {
  client: BSSOAuthClient;
  client_secret: string;
}

// --- VPN/tunnel concentrator (admin-platform backlog, deliberately last) ---
export interface VPNPeer {
  id: string;
  device_id: string;
  public_key: string;
  private_key?: string; // only ever present on enroll/config responses, never in the list
  overlay_ip: string;
  status: "ENROLLED" | "REVOKED";
  created_at: string;
  revoked_at?: string;
}

export interface VPNEnrollResult {
  peer: VPNPeer;
  config: string;
}

export interface VPNConcentrator {
  server_public_key: string;
  endpoint: string;
  overlay_subnet: string;
  configured: boolean;
}
