import { describe, expect, it } from "vitest";
import { isMFAChallenge } from "../api/client";
import { groupSecret } from "./mfa";

describe("two-step sign-in helpers", () => {
  it("groups the key in fours", () => {
    expect(groupSecret("JBSWY3DPEHPK3PXP")).toBe("JBSW Y3DP EHPK 3PXP");
    expect(groupSecret("ABCDEFGHIJ")).toBe("ABCD EFGH IJ");
  });

  it("tells a challenge from a session", () => {
    expect(isMFAChallenge({ mfa: "verify", mfa_token: "t", expires_at: "x" })).toBe(true);
    expect(isMFAChallenge({ token: "t", role: "noc", expires_at: "x" })).toBe(false);
  });
});
