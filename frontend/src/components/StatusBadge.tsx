// One component covering every enum-like field in the app (device
// online_status, job status, connection_request_mode) so the color
// mapping stays centralized — build plan §3.4.
const TONE_BY_VALUE: Record<string, string> = {
  ONLINE: "ok",
  SUCCESS: "ok",
  HTTP_200_INFORM_RECEIVED: "ok",
  DIRECT_IPV4: "ok",
  DIRECT_IPV6: "ok",
  QUEUED: "pending",
  RPC_SENT: "pending",
  IN_PROGRESS: "pending",
  UNKNOWN: "neutral",
  FAILED: "danger",
  TIMEOUT: "danger",
  OFFLINE: "danger",
  UNREACHABLE: "danger",
  PERIODIC_FALLBACK_ONLY: "warn",
  // Firmware rollout states (build plan §4 Phase 7 / design doc v3 §9.5)
  ELIGIBLE: "neutral",
  DOWNLOADING: "pending",
  ACTIVE: "pending",
  BLOCKED: "danger",
  COMPLETED: "ok",
  ABORTED: "danger",
  DRAFT: "neutral",
  // Credential rotation states (build plan §4 Phase 6 / design doc v3 §11.6)
  PENDING: "pending",
  GRACE: "warn",
  REVOKED: "neutral",
};

export function StatusBadge({ value }: { value: string }) {
  const tone = TONE_BY_VALUE[value] ?? "neutral";
  return <span className={`pill pill-${tone}`}>{value}</span>;
}
