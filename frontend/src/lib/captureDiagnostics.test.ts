import { describe, expect, it } from "vitest";
import type { CaptureEvent } from "../api/types";
import { diagnoseCapture } from "./captureDiagnostics";

function event(overrides: Partial<CaptureEvent>): CaptureEvent {
  return {
    id: "evt-1",
    seq: 1,
    direction: "inbound",
    kind: "AuthenticationFailure",
    occurred_at: "2026-09-15T10:00:00Z",
    summary: "CWMP authentication failed",
    ...overrides,
  };
}

describe("diagnoseCapture", () => {
  it("separates no traffic from authentication failure", () => {
    const empty = diagnoseCapture([]);
    expect(empty.headline).toContain("No matching CPE traffic");
    expect(empty.stages.find((stage) => stage.id === "traffic")?.state).toBe("pending");

    const auth = diagnoseCapture([event({})]);
    expect(auth.headline).toContain("authentication is the blocker");
    expect(auth.stages.find((stage) => stage.id === "traffic")?.state).toBe("pass");
    expect(auth.stages.find((stage) => stage.id === "authentication")?.state).toBe("warn");
  });

  it("recognizes an authenticated Huawei N5368X Inform", () => {
    const result = diagnoseCapture([
      event({
        kind: "Inform",
        summary: "Inform (events: [0 BOOTSTRAP])",
        body: "DeviceId: OUI=ABCDEF ProductClass=N5368X SerialNumber=12345",
      }),
    ]);

    expect(result.huaweiN5368).toBe(true);
    expect(result.headline).toContain("Onboarding/authentication succeeded");
    expect(result.stages.find((stage) => stage.id === "authentication")?.state).toBe("pass");
  });

  it("maps a 9005 fault to model rediscovery guidance", () => {
    const result = diagnoseCapture([
      event({ kind: "Inform", summary: "Inform", body: "DeviceId: OUI=ABCDEF ProductClass=HG8145V5 SerialNumber=1" }),
      event({
        id: "evt-2",
        seq: 2,
        direction: "outbound",
        kind: "GetParameterValues",
        summary: "GetParameterValues",
      }),
      event({
        id: "evt-3",
        seq: 3,
        direction: "inbound",
        kind: "Fault",
        summary: "CWMP Fault 9005 Invalid Parameter Name",
      }),
    ]);

    expect(result.faultCodes).toContain("9005");
    expect(result.headline).toContain("unsupported");
    expect(result.nextStep).toContain("PreSharedKey.1.KeyPassphrase");
  });
});
