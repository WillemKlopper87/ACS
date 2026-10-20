import type { Credential, Device, Job, ParameterCache } from "../api/types";

export type OnboardingCheckStatus = "pass" | "warn" | "fail" | "pending";
export type TroubleshootingSeverity = "info" | "warning" | "critical";
export type VendorFamily = "Huawei" | "Nokia" | "Other";

export interface OnboardingCheck {
  id: string;
  label: string;
  status: OnboardingCheckStatus;
  detail: string;
}

export interface TroubleshootingHint {
  id: string;
  severity: TroubleshootingSeverity;
  title: string;
  detail: string;
}

export interface DeviceOnboardingAssessment {
  vendorFamily: VendorFamily;
  isHuawei: boolean;
  isHuaweiN5368X: boolean;
  isNokia: boolean;
  healthyChecks: number;
  totalChecks: number;
  checks: OnboardingCheck[];
  hints: TroubleshootingHint[];
  recentFailures: Job[];
}

const FAILURE_STATUSES = new Set(["FAILED", "TIMEOUT"]);
const SUCCESSFUL_CONNECTION_REQUEST = new Set(["SUCCESS", "SUCCEEDED", "ACCEPTED", "200", "202", "204"]);

function normalized(value?: string) {
  return (value ?? "").trim().toLowerCase();
}

function isKnownRoot(root?: string) {
  return root === "Device." || root === "InternetGatewayDevice.";
}

function privateConnectionRequest(url?: string) {
  if (!url) return false;
  try {
    const host = new URL(url).hostname.toLowerCase();
    if (host === "localhost" || host === "::1" || host.startsWith("127.")) return true;
    if (host.startsWith("10.") || host.startsWith("192.168.")) return true;
    const match = host.match(/^172\.(\d+)\./);
    return match ? Number(match[1]) >= 16 && Number(match[1]) <= 31 : false;
  } catch {
    return false;
  }
}

function newestCredential(credentials: Credential[], type: Credential["credential_type"], status?: Credential["status"]) {
  return credentials.find((credential) =>
    credential.credential_type === type && (!status || credential.status === status),
  );
}

function faultHint(job: Job): TroubleshootingHint | null {
  const code = (job.fault_code ?? "").trim();
  const detail = (job.fault_string ?? "").trim();

  switch (code) {
    case "9001":
      return {
        id: `fault-${job.command_key}`,
        severity: "warning",
        title: "CPE rejected the request (9001)",
        detail: `${job.type} was denied by the device${detail ? `: ${detail}` : "."} Check vendor policy/permissions before retrying.`,
      };
    case "9002":
      return {
        id: `fault-${job.command_key}`,
        severity: "warning",
        title: "CPE internal error (9002)",
        detail: `${job.type} reached the CPE but it reported an internal error${detail ? `: ${detail}` : "."} Capture the session and record the exact firmware.`,
      };
    case "9003":
      return {
        id: `fault-${job.command_key}`,
        severity: "warning",
        title: "Invalid arguments (9003)",
        detail: `${job.type} used an argument, value type, or path the firmware rejected${detail ? `: ${detail}` : "."} Re-read the parameter model before another write.`,
      };
    case "9005":
      return {
        id: `fault-${job.command_key}`,
        severity: "warning",
        title: "Unsupported parameter path (9005)",
        detail: `${job.type} referenced a path this firmware does not expose${detail ? `: ${detail}` : "."} Run parameter discovery and use the discovered vendor/model path rather than assuming TR-181/TR-098 equivalence.`,
      };
    case "9007":
      return {
        id: `fault-${job.command_key}`,
        severity: "warning",
        title: "Invalid parameter value (9007)",
        detail: `${job.type} reached the parameter but the value/type was rejected${detail ? `: ${detail}` : "."} Read the current value and vendor constraints before retrying.`,
      };
    default:
      if (job.status === "TIMEOUT") {
        return {
          id: `timeout-${job.command_key}`,
          severity: "warning",
          title: `${job.type} timed out`,
          detail: "Start a CWMP capture before retrying. A timeout can mean no new session, a lost response, or a CPE that stopped processing after the ACS RPC.",
        };
      }
      if (job.status === "FAILED") {
        return {
          id: `failed-${job.command_key}`,
          severity: "warning",
          title: `${job.type} failed`,
          detail: detail || "Inspect the job and a fresh session capture before retrying; do not change fleet-wide compatibility settings from one unexplained failure.",
        };
      }
      return null;
  }
}

export function assessDeviceOnboarding(
  device: Device,
  params: ParameterCache,
  jobs: Job[],
  credentials: Credential[] = [],
): DeviceOnboardingAssessment {
  const manufacturer = normalized(device.manufacturer);
  const model = normalized(device.product_class);
  const isHuawei = manufacturer.includes("huawei") || model.includes("huawei");
  const isHuaweiN5368X = isHuawei && /n5368x?|5g\s*cpe\s*pro/.test(model);
  const isNokia = manufacturer.includes("nokia") || model.includes("nokia") || model.includes("fastmile");
  const vendorFamily: VendorFamily = isHuawei ? "Huawei" : isNokia ? "Nokia" : "Other";
  const recentFailures = jobs.filter((job) => FAILURE_STATUSES.has(job.status)).slice(0, 5);
  const lastCR = (device.last_connection_request_status ?? "").trim().toUpperCase();
  const parameterCount = Object.keys(params).length;

  const activeDigest = newestCredential(credentials, "CWMP_DIGEST", "ACTIVE");
  const pendingDigest = newestCredential(credentials, "CWMP_DIGEST", "PENDING");
  const graceDigest = newestCredential(credentials, "CWMP_DIGEST", "GRACE");

  const checks: OnboardingCheck[] = [
    {
      id: "identity",
      label: "Identity",
      status: device.oui && device.serial_number ? "pass" : "fail",
      detail: device.oui && device.serial_number
        ? `${device.oui} + ${device.serial_number}`
        : "OUI and serial number are required to bind sessions safely to one physical CPE.",
    },
    {
      id: "inform",
      label: "Authenticated Inform",
      status: device.last_inform_at ? "pass" : "fail",
      detail: device.last_inform_at
        ? `Last Inform: ${device.last_inform_at}`
        : "No successful Inform is recorded. Prove reachability/authentication before testing parameters.",
    },
    {
      id: "auth",
      label: "Authentication",
      status: device.cwmp_auth_mode && normalized(device.cwmp_auth_mode) !== "none" ? "pass" : "warn",
      detail: device.cwmp_auth_mode
        ? `Observed mode: ${device.cwmp_auth_mode}`
        : "Authentication mode has not been learned yet.",
    },
    {
      id: "bound-credential",
      label: "Per-device CWMP credential",
      status: activeDigest ? "pass" : pendingDigest ? "pending" : graceDigest ? "warn" : "warn",
      detail: activeDigest
        ? `CWMP_DIGEST v${activeDigest.version} ACTIVE — CPE→ACS identity is device-bound.`
        : pendingDigest
          ? `CWMP_DIGEST v${pendingDigest.version} PENDING — waiting for a reconnect authenticated with the generated credential and matching device identity.`
          : graceDigest
            ? `CWMP_DIGEST v${graceDigest.version} is in GRACE; confirm the intended ACTIVE credential before further provisioning.`
            : "No per-device CWMP_DIGEST credential is visible yet. In the secure bootstrap flow normal management waits for credential graduation.",
    },
    {
      id: "model",
      label: "Data model root",
      status: isKnownRoot(device.data_model_root) ? "pass" : "warn",
      detail: isKnownRoot(device.data_model_root)
        ? device.data_model_root
        : "Root is unknown/non-standard. Discover the parameter model before any write.",
    },
    {
      id: "parameters",
      label: "Parameter evidence",
      status: parameterCount > 0 ? "pass" : "pending",
      detail: parameterCount > 0
        ? `${parameterCount} cached parameter${parameterCount === 1 ? "" : "s"}`
        : "No parameter values are cached yet. Run discovery and then a live read.",
    },
    {
      id: "connection-request",
      label: "Connection Request",
      status: !device.connection_request_url
        ? "warn"
        : !lastCR
          ? "pending"
          : SUCCESSFUL_CONNECTION_REQUEST.has(lastCR)
            ? "pass"
            : "warn",
      detail: !device.connection_request_url
        ? "No Connection Request URL learned from Inform."
        : !lastCR
          ? `${device.connection_request_mode || "unknown"} reachability is configured but has not been tested.`
          : `${device.connection_request_mode || "unknown"}: ${device.last_connection_request_status}`,
    },
    {
      id: "operations",
      label: "Recent ACS operations",
      status: recentFailures.length > 0 ? "warn" : jobs.length > 0 ? "pass" : "pending",
      detail: recentFailures.length > 0
        ? `${recentFailures.length} recent failed/timed-out operation${recentFailures.length === 1 ? "" : "s"}`
        : jobs.length > 0
          ? "No failed/timed-out operation in the loaded recent job set."
          : "No ACS-initiated operation has been proven yet.",
    },
  ];

  const hints: TroubleshootingHint[] = [];

  if (!device.last_inform_at) {
    hints.push({
      id: "no-inform",
      severity: "critical",
      title: "Separate reachability from authentication first",
      detail: "Start a remote-IP CWMP capture before the next attempt. The capture can prove the HTTP request reached ACS and whether authentication failed. ACS_ONBOARDING_LISTENER=once can add host-log reachability evidence without disabling authentication.",
    });
  }

  if (pendingDigest) {
    hints.push({
      id: "digest-pending",
      severity: "warning",
      title: "Credential graduation is waiting for a bound reconnect",
      detail: "Do not manually activate this CWMP_DIGEST credential. The CPE must reconnect using it and report the same OUI/ProductClass/Serial identity; only that authenticated Inform may promote it to ACTIVE.",
    });
  }

  if (isHuaweiN5368X) {
    hints.push({
      id: "huawei-n5368x-digest",
      severity: "info",
      title: "Huawei N5368X: keep Digest-SHA256 enabled",
      detail: "ACS supports SHA-256 in both CPE→ACS and ACS→CPE Connection Request directions. If this firmware loops/stops after the first 401, capture the exchange first; in an isolated test deployment try ACS_DIGEST_ALGORITHMS=SHA-256 to emit one minimal Digest challenge before considering Basic auth.",
    });
  } else if (isHuawei) {
    hints.push({
      id: "huawei-auth",
      severity: "info",
      title: "Huawei: diagnose the 401 exchange before weakening authentication",
      detail: "Huawei embedded HTTP stacks vary by firmware. Use a capture to determine whether the CPE retries the Digest challenge. Prefer a single supported Digest algorithm for a problem firmware before enabling Basic, and keep any weaker setting isolated to the affected test profile.",
    });
  }

  if (isHuawei && device.data_model_root === "InternetGatewayDevice.") {
    hints.push({
      id: "huawei-tr098-wifi",
      severity: "warning",
      title: "Huawei TR-098 Wi-Fi paths can be deceptively writable",
      detail: "Several EchoLife firmwares advertise WLANConfiguration.{i}.KeyPassphrase but reject writes. Prefer discovered writable PreSharedKey.1.KeyPassphrase paths and never assume a bare advertised path is writable.",
    });
  }

  if (isNokia) {
    hints.push({
      id: "nokia-read-only-first",
      severity: "info",
      title: "Nokia: keep the first qualification read-only",
      detail: "Use parameter discovery and the Device Compatibility evidence before any vendor-specific write. Read only paths that this exact firmware exposes; do not promote guessed Nokia 5G paths from another model or build.",
    });
    if (privateConnectionRequest(device.connection_request_url)) {
      hints.push({
        id: "nokia-private-cr",
        severity: "warning",
        title: "Nokia Connection Request URL is private",
        detail: `${device.connection_request_url} is not directly reachable from a public cloud ACS. Prove normal periodic Inform first, then qualify NAT/STUN/Annex-G or another reachable Connection Request path instead of rewriting the URL blindly.`,
      });
    }
  }

  if (!device.connection_request_url) {
    hints.push({
      id: "no-cr-url",
      severity: "warning",
      title: "Connection Request URL is missing",
      detail: "Inspect the latest Inform/parameter cache for ManagementServer.ConnectionRequestURL (TR-181 or TR-098 form). Do not debug outbound Digest until the CPE has actually supplied a reachable URL.",
    });
  } else if (lastCR && !SUCCESSFUL_CONNECTION_REQUEST.has(lastCR)) {
    hints.push({
      id: "cr-failed",
      severity: "warning",
      title: "Connection Request has not succeeded",
      detail: "Start a CWMP capture, retry Connection Request, and verify that Event 6 CONNECTION REQUEST Inform follows. Compare the CPE's advertised authentication/NAT behavior with the observed request before changing credentials.",
    });
  }

  for (const job of recentFailures) {
    const hint = faultHint(job);
    if (hint) hints.push(hint);
  }

  const dedupedHints = hints.filter((hint, index, all) => {
    const key = hint.title.replace(/\s*\([^)]*\)$/, "");
    return all.findIndex((candidate) => candidate.title.replace(/\s*\([^)]*\)$/, "") === key) === index;
  });

  return {
    vendorFamily,
    isHuawei,
    isHuaweiN5368X,
    isNokia,
    healthyChecks: checks.filter((check) => check.status === "pass").length,
    totalChecks: checks.length,
    checks,
    hints: dedupedHints,
    recentFailures,
  };
}
