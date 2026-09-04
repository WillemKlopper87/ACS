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
  // A non-repeatable job whose lease expired and which was deliberately
  // NOT retried (internal/jobs/lease.go). It means the work did not run
  // and no longer will — the status most in need of operator attention,
  // so it must not fall through to the benign neutral default.
  LEASE_EXPIRED_UNSAFE_RETRY: "danger",
  // Firmware rollout states (build plan §4 Phase 7 / design doc v3 §9.5)
  ELIGIBLE: "neutral",
  DOWNLOADING: "pending",
  AWAITING_TRANSFER_COMPLETE: "pending",
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

// Human-readable text for the values whose wire form is a mouthful.
// Only the genuinely unreadable ones are mapped — ONLINE/FAILED/QUEUED
// and friends already read fine and stay verbatim, so the badge text
// still matches what operators see in logs and the API for those. The
// raw value is always available on hover via title.
const LABEL_BY_VALUE: Record<string, string> = {
  HTTP_200_INFORM_RECEIVED: "inform received",
  PERIODIC_FALLBACK_ONLY: "periodic fallback",
  LEASE_EXPIRED_UNSAFE_RETRY: "expired — not retried",
  AWAITING_TRANSFER_COMPLETE: "awaiting transfer",
  DIRECT_IPV4: "direct IPv4",
  DIRECT_IPV6: "direct IPv6",
  IN_PROGRESS: "in progress",
  RPC_SENT: "RPC sent",
};

export function StatusBadge({ value }: { value: string }) {
  const tone = TONE_BY_VALUE[value] ?? "neutral";
  const label = LABEL_BY_VALUE[value];
  return (
    <span className={`pill pill-${tone}`} title={value}>
      {label ?? value}
    </span>
  );
}
