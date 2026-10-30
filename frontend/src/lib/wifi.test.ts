import { describe, expect, it } from "vitest";
import { wifiInputValid } from "./wifi";

describe("wifiInputValid", () => {
  it("needs an SSID or a password", () => {
    expect(wifiInputValid("", "")).toBe(false);
    expect(wifiInputValid("   ", "")).toBe(false);
    expect(wifiInputValid("SENT-5G", "")).toBe(true);
    expect(wifiInputValid("", "correct-horse")).toBe(true);
  });

  it("enforces the 32-byte SSID and 8-63 character passphrase limits", () => {
    expect(wifiInputValid("x".repeat(33), "")).toBe(false);
    expect(wifiInputValid("é".repeat(17), "")).toBe(false); // 34 bytes
    expect(wifiInputValid("Home", "short")).toBe(false);
    expect(wifiInputValid("Home", "p".repeat(64))).toBe(false);
  });
});
