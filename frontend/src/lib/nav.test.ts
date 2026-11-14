import { describe, expect, it } from "vitest";
import { AREAS, areaOf, screenTitle, visibleAreas } from "./nav";

describe("navigation areas", () => {
  it("places every screen in exactly one area", () => {
    const ids = AREAS.flatMap((a) => a.screens.map((s) => s.id));
    expect(new Set(ids).size).toBe(ids.length);
    // Every screen that used to be in the sidebar is still reachable.
    for (const id of ["dashboard", "health", "jobs", "alerting", "fleet", "onboarding", "control", "operations", "groups", "templates", "policies", "schedules", "rollouts", "captures", "bss", "operators", "tenancy", "audit", "reports", "help"]) {
      expect(ids).toContain(id);
    }
  });

  it("hides the administration area from non-admins", () => {
    expect(visibleAreas(true).map((a) => a.id)).toContain("administration");
    expect(visibleAreas(false).map((a) => a.id)).not.toContain("administration");
    // Nothing else is hidden.
    expect(visibleAreas(false).length).toBe(AREAS.length - 1);
  });

  it("leaves the BSS / NaaS screen out when the ACS runs on its own", () => {
    const withNaas = visibleAreas(true).flatMap((a) => a.screens.map((s) => s.id));
    const standalone = visibleAreas(true, true).flatMap((a) => a.screens.map((s) => s.id));
    expect(withNaas).toContain("bss");
    expect(standalone).not.toContain("bss");
    // Only that screen goes; every other one is still there.
    expect(standalone.length).toBe(withNaas.length - 1);
    expect(standalone).toContain("operators");
  });

  it("finds a screen's area, with a device's pages under Devices", () => {
    expect(areaOf("operations")?.id).toBe("activity");
    expect(areaOf("devices")?.id).toBe("devices");
    expect(areaOf("account")).toBeUndefined();
  });

  it("names every screen", () => {
    expect(screenTitle("schedules")).toBe("Scheduled jobs");
    expect(screenTitle("devices")).toBe("Device");
    expect(screenTitle("account")).toBe("Account");
  });
});
