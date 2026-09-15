import type { Device, Job, ParameterCache } from "../api/types";
import { assessDeviceOnboarding, type OnboardingCheckStatus, type TroubleshootingSeverity } from "../lib/deviceOnboarding";
import { StatusBadge } from "./StatusBadge";

const CHECK_LABEL: Record<OnboardingCheckStatus, string> = {
  pass: "PASS",
  warn: "CHECK",
  fail: "FAIL",
  pending: "PENDING",
};

const HINT_LABEL: Record<TroubleshootingSeverity, string> = {
  info: "Huawei note",
  warning: "Investigate",
  critical: "Start here",
};

export function DeviceOnboardingPanel({
  device,
  params,
  jobs,
  writable,
  busy,
  onStartCWMPTrace,
  onDiscoverParameters,
  onTestConnectionRequest,
  onRefreshCellular,
  onOpenCaptures,
}: {
  device: Device;
  params: ParameterCache;
  jobs: Job[];
  writable: boolean;
  busy: boolean;
  onStartCWMPTrace: () => void;
  onDiscoverParameters: () => void;
  onTestConnectionRequest: () => void;
  onRefreshCellular: () => void;
  onOpenCaptures: () => void;
}) {
  const assessment = assessDeviceOnboarding(device, params, jobs);
  const ready = assessment.healthyChecks === assessment.totalChecks;

  return (
    <div className="panel" style={{ gridColumn: "1 / -1" }}>
      <h3 style={{ alignItems: "flex-start" }}>
        <span>
          Onboarding &amp; troubleshooting
          {assessment.isHuawei && (
            <span className="chip" style={{ marginLeft: "0.55rem", verticalAlign: "middle" }}>
              Huawei{assessment.isHuaweiN5368X ? " · N5368X family" : ""}
            </span>
          )}
        </span>
        <span style={{ display: "flex", gap: "0.45rem", alignItems: "center" }}>
          <StatusBadge value={ready ? "READY" : "CHECK"} />
          <span className="dim" style={{ fontSize: "0.78rem" }}>
            {assessment.healthyChecks}/{assessment.totalChecks} healthy
          </span>
        </span>
      </h3>

      <p className="dim" style={{ marginTop: 0, marginBottom: "0.85rem", fontSize: "0.82rem" }}>
        Use this sequence before changing credentials or vendor compatibility settings. It separates transport/authentication,
        CWMP model discovery, reachability and device-operation faults so a Huawei firmware quirk does not become a fleet-wide workaround.
      </p>

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(210px, 1fr))",
          gap: "0.55rem",
          marginBottom: "0.85rem",
        }}
      >
        {assessment.checks.map((check) => (
          <div key={check.id} style={{ border: "1px solid var(--border)", borderRadius: "8px", padding: "0.65rem" }}>
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "0.5rem" }}>
              <strong style={{ fontSize: "0.82rem" }}>{check.label}</strong>
              <StatusBadge value={CHECK_LABEL[check.status]} />
            </div>
            <div className="dim" style={{ fontSize: "0.75rem", marginTop: "0.35rem", lineHeight: 1.35 }}>
              {check.detail}
            </div>
          </div>
        ))}
      </div>

      {assessment.hints.length > 0 && (
        <div style={{ display: "grid", gap: "0.5rem", marginBottom: "0.85rem" }}>
          {assessment.hints.slice(0, 5).map((hint) => (
            <div
              key={hint.id}
              className={hint.severity === "critical" ? "banner error" : "banner"}
              style={{ margin: 0, padding: "0.65rem 0.75rem" }}
            >
              <div style={{ display: "flex", gap: "0.55rem", alignItems: "baseline", flexWrap: "wrap" }}>
                <strong>{hint.title}</strong>
                <span className="dim" style={{ fontSize: "0.7rem", textTransform: "uppercase", letterSpacing: "0.04em" }}>
                  {HINT_LABEL[hint.severity]}
                </span>
              </div>
              <div style={{ marginTop: "0.25rem", fontSize: "0.78rem", lineHeight: 1.4 }}>{hint.detail}</div>
            </div>
          ))}
        </div>
      )}

      <div style={{ display: "flex", gap: "0.5rem", flexWrap: "wrap" }}>
        <button className="btn primary" onClick={onStartCWMPTrace} disabled={busy || !writable} title="Start a bounded, redacted CWMP session capture before reproducing the fault">
          1. Start CWMP trace
        </button>
        <button className="btn" onClick={onDiscoverParameters} disabled={busy || !writable} title="Queues GetParameterNames discovery; use the learned model before writes">
          2. Discover model
        </button>
        <button className="btn" onClick={onTestConnectionRequest} disabled={busy || !writable || !device.connection_request_url} title={device.connection_request_url ? "Test CPE reachability and watch for Event 6 Inform" : "Unavailable until the CPE supplies a Connection Request URL"}>
          3. Test Connection Request
        </button>
        {assessment.isHuawei && (
          <button className="btn" onClick={onRefreshCellular} disabled={busy || !writable} title="Read the vendor-aware cellular diagnostic candidate paths">
            Huawei cellular read
          </button>
        )}
        <button className="btn ghost" onClick={onOpenCaptures}>
          Open captures
        </button>
      </div>

      {!writable && (
        <p className="dim" style={{ fontSize: "0.74rem", marginBottom: 0 }}>
          Your role can inspect this diagnosis, but starting captures or queueing device work requires write access.
        </p>
      )}
    </div>
  );
}
