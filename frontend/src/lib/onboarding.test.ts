import { describe, expect, it } from "vitest";
import type { Onboarding } from "../api/client";
import { onboardingTab, onboardingTimeline, parsePLMNs, stateTone } from "./onboarding";

const base: Onboarding = {
  id: "ob-1",
  expected_serial: "CSC25430018F",
  state: "PLANNED",
  state_reason: "",
  role: "gateway",
  commercial_model: "retail",
  source: "console",
  config_jobs: [],
  created_at: "2026-10-30T10:00:00Z",
  updated_at: "2026-10-30T10:00:00Z",
};

const statuses = (o: Partial<Onboarding>) => onboardingTimeline({ ...base, ...o }).map((s) => `${s.id}:${s.status}`);

describe("onboardingTab", () => {
  it("puts what needs an operator first, and closes finished work", () => {
    expect(onboardingTab("ON_HOLD")).toBe("attention");
    expect(onboardingTab("FAILED")).toBe("attention");
    expect(onboardingTab("CONFIGURING")).toBe("progress");
    expect(onboardingTab("PLANNED")).toBe("planned");
    expect(onboardingTab("IN_SERVICE")).toBe("completed");
    expect(onboardingTab("CANCELLED")).toBe("completed");
  });
  it("colours by what the operator must do", () => {
    expect(stateTone("IN_SERVICE")).toBe("pill-ok");
    expect(stateTone("ON_HOLD")).toBe("pill-warn");
    expect(stateTone("FAILED")).toBe("pill-danger");
    expect(stateTone("SECURED")).toBe("pill-pending");
  });
});

describe("onboardingTimeline", () => {
  it("waits for first contact while planned", () => {
    expect(statuses({})).toEqual([
      "planned:done", "contact:current", "sim:pending", "secured:pending", "discovery:pending", "configuration:pending", "service:pending",
    ]);
  });

  it("shows which check a contacted device is waiting on", () => {
    const sim = statuses({ state: "CONTACTED", state_reason: "waiting for the SIM identifiers", contacted_at: "2026-10-30T10:05:00Z" });
    expect(sim.slice(1, 4)).toEqual(["contact:done", "sim:current", "secured:pending"]);
    const cred = statuses({ state: "CONTACTED", state_reason: "waiting for the device to authenticate with its own credential", contacted_at: "x" });
    expect(cred.slice(2, 4)).toEqual(["sim:done", "secured:current"]);
  });

  it("marks where a held or failed onboarding stopped", () => {
    expect(statuses({ state: "ON_HOLD", state_reason: "SIM does not match the sale", contacted_at: "x" })[2]).toBe("sim:blocked");
    // Held before it was ever bound: the device belongs to another customer.
    expect(statuses({ state: "ON_HOLD", state_reason: "the device already belongs to another customer" })[1]).toBe("contact:blocked");
    expect(statuses({ state: "FAILED", state_reason: "configuration failed: fault 9007", contacted_at: "x" })[5]).toBe("configuration:blocked");
  });

  it("is all done in service", () => {
    expect(statuses({ state: "IN_SERVICE", contacted_at: "x", in_service_at: "y" }).every((s) => s.endsWith(":done"))).toBe(true);
  });
});

describe("parsePLMNs", () => {
  it("accepts 5 and 6 digit MCC+MNC separated by commas or spaces", () => {
    expect(parsePLMNs("65506, 655010 65502")).toEqual({ plmns: ["65506", "655010", "65502"], invalid: [] });
    expect(parsePLMNs("")).toEqual({ plmns: [], invalid: [] });
  });
  it("reports what is not one", () => {
    expect(parsePLMNs("65506 MTN 6550").invalid).toEqual(["MTN", "6550"]);
  });
});
