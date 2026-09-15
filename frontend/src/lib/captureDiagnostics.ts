import type { CaptureEvent } from "../api/types";

export type CaptureStageState = "pass" | "warn" | "pending";

export interface CaptureStage {
  id: string;
  label: string;
  state: CaptureStageState;
  detail: string;
}

export interface CaptureDiagnosis {
  stages: CaptureStage[];
  headline: string;
  nextStep: string;
  huaweiN5368: boolean;
  faultCodes: string[];
}

function text(event: CaptureEvent) {
  return `${event.kind} ${event.summary} ${event.body ?? ""}`;
}

export function diagnoseCapture(events: CaptureEvent[]): CaptureDiagnosis {
  const joined = events.map(text).join("\n");
  const authenticationFailures = events.filter((event) => event.kind === "AuthenticationFailure");
  const informs = events.filter((event) => event.kind === "Inform");
  const outbound = events.filter((event) => event.direction === "outbound");
  const huaweiN5368 = /ProductClass\s*=\s*[^\n]*(?:N5368X?|5G\s*CPE\s*Pro)/i.test(joined);
  const faultCodes = Array.from(new Set(joined.match(/\b900[0-9]\b/g) ?? []));

  const stages: CaptureStage[] = [
    {
      id: "traffic",
      label: "Traffic reached ACS",
      state: events.length > 0 ? "pass" : "pending",
      detail: events.length > 0
        ? `${events.length} protocol event${events.length === 1 ? "" : "s"} recorded.`
        : "No matching traffic has been recorded yet.",
    },
    {
      id: "authentication",
      label: "Authentication",
      state: informs.length > 0 ? "pass" : authenticationFailures.length > 0 ? "warn" : "pending",
      detail: informs.length > 0
        ? "An authenticated Inform was accepted."
        : authenticationFailures.length > 0
          ? `${authenticationFailures.length} authentication failure${authenticationFailures.length === 1 ? "" : "s"}; transport reached ACS but the CPE did not authenticate.`
          : "No authentication result is visible yet.",
    },
    {
      id: "inform",
      label: "CWMP session start",
      state: informs.length > 0 ? "pass" : "pending",
      detail: informs.length > 0
        ? "Device identity and Inform event codes were captured."
        : "No Inform has been accepted yet.",
    },
    {
      id: "rpc",
      label: "ACS/CPE RPC exchange",
      state: outbound.length > 0 ? (faultCodes.length > 0 ? "warn" : "pass") : "pending",
      detail: outbound.length > 0
        ? faultCodes.length > 0
          ? `RPC traffic is present, with fault code${faultCodes.length === 1 ? "" : "s"} ${faultCodes.join(", ")}.`
          : `${outbound.length} outbound ACS event${outbound.length === 1 ? "" : "s"} recorded.`
        : "No ACS-initiated RPC is visible yet.",
    },
  ];

  if (events.length === 0) {
    return {
      stages,
      headline: "No matching CPE traffic yet",
      nextStep: "Check the CPE ACS URL, DNS/IP route, firewall/NAT and TLS. For a controlled field test, ACS_ONBOARDING_LISTENER=once adds host-log proof that a POST reached the normal CWMP endpoint without disabling authentication.",
      huaweiN5368,
      faultCodes,
    };
  }

  if (authenticationFailures.length > 0 && informs.length === 0) {
    return {
      stages,
      headline: "CPE reaches ACS but authentication is the blocker",
      nextStep: "Keep the capture running and reproduce one clean retry. For Huawei firmware, inspect whether it answers the Digest 401 before enabling weaker auth. N5368X-class devices should use SHA-256; in an isolated test deployment, ACS_DIGEST_ALGORITHMS=SHA-256 can reduce the challenge to one minimal Digest line.",
      huaweiN5368,
      faultCodes,
    };
  }

  if (informs.length > 0 && outbound.length === 0) {
    return {
      stages,
      headline: "Onboarding/authentication succeeded; prove the data model next",
      nextStep: "Run parameter discovery and a small live read before any write. Then test Connection Request and confirm that Event 6 CONNECTION REQUEST Inform returns to the ACS.",
      huaweiN5368,
      faultCodes,
    };
  }

  if (faultCodes.includes("9005")) {
    return {
      stages,
      headline: "The session works, but the requested path is unsupported",
      nextStep: "Re-run GetParameterNames discovery and use the exact discovered path. On Huawei TR-098/EchoLife firmware, do not assume an advertised WLANConfiguration KeyPassphrase is writable; prefer a discovered writable PreSharedKey.1.KeyPassphrase path.",
      huaweiN5368,
      faultCodes,
    };
  }

  if (faultCodes.length > 0) {
    return {
      stages,
      headline: "CWMP exchange is working but the CPE returned a fault",
      nextStep: `Investigate ${faultCodes.join(", ")} against the exact firmware and RPC payload. Preserve this capture and avoid turning a device-specific workaround into a fleet default.`,
      huaweiN5368,
      faultCodes,
    };
  }

  return {
    stages,
    headline: "Capture shows a working CWMP exchange",
    nextStep: huaweiN5368
      ? "For this Huawei N5368-family device, continue with parameter discovery, a reversible read/write, Connection Request SHA-256 verification and recovery testing before marking the firmware supported."
      : "Continue with parameter discovery, a reversible read/write, Connection Request and recovery testing before marking this firmware supported.",
    huaweiN5368,
    faultCodes,
  };
}
