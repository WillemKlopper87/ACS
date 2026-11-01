// Presentation rules for onboarding (docs/ONBOARDING.md): which tab a state
// belongs to, its colour, and the step-by-step timeline. The states and
// their reasons come from cmd/acs (internal/onboarding.Evaluate); this file
// only reads them.
import type { Onboarding, OnboardingState } from "../api/client";

export type OnboardingTabId = "attention" | "progress" | "planned" | "completed";

export const ONBOARDING_TABS: { id: OnboardingTabId; label: string; states: OnboardingState[] }[] = [
  { id: "attention", label: "Needs attention", states: ["ON_HOLD", "FAILED"] },
  { id: "progress", label: "In progress", states: ["CONTACTED", "SECURED", "DISCOVERED", "CONFIGURING"] },
  { id: "planned", label: "Planned", states: ["PLANNED"] },
  { id: "completed", label: "Completed", states: ["IN_SERVICE", "CANCELLED"] },
];

export function onboardingTab(state: OnboardingState): OnboardingTabId {
  return ONBOARDING_TABS.find((t) => t.states.includes(state))?.id ?? "progress";
}

export const STATE_LABEL: Record<OnboardingState, string> = {
  PLANNED: "Planned",
  CONTACTED: "Contacted",
  SECURED: "Secured",
  DISCOVERED: "Discovered",
  CONFIGURING: "Configuring",
  IN_SERVICE: "In service",
  ON_HOLD: "On hold",
  FAILED: "Failed",
  CANCELLED: "Cancelled",
};

export function stateTone(state: OnboardingState): string {
  switch (state) {
    case "IN_SERVICE":
      return "pill-ok";
    case "ON_HOLD":
      return "pill-warn";
    case "FAILED":
      return "pill-danger";
    case "PLANNED":
    case "CANCELLED":
      return "pill-neutral";
    default:
      return "pill-pending";
  }
}

export type StepStatus = "done" | "current" | "blocked" | "pending";
export interface TimelineStep {
  id: "planned" | "contact" | "sim" | "secured" | "discovery" | "configuration" | "service";
  label: string;
  status: StepStatus;
}

const STEPS: { id: TimelineStep["id"]; label: string }[] = [
  { id: "planned", label: "Sale recorded" },
  { id: "contact", label: "First contact" },
  { id: "sim", label: "SIM and network check" },
  { id: "secured", label: "Own credential" },
  { id: "discovery", label: "Parameter discovery" },
  { id: "configuration", label: "Plan configuration" },
  { id: "service", label: "In service" },
];

/** Index of the step an onboarding is at (or stopped at). */
function position(o: Onboarding): number {
  const simWait = /SIM/i.test(o.state_reason ?? "");
  switch (o.state) {
    case "PLANNED":
      return 1;
    case "CONTACTED":
      return simWait ? 2 : 3;
    case "SECURED":
      return 4;
    case "DISCOVERED":
    case "CONFIGURING":
    case "FAILED":
      return 5;
    case "ON_HOLD":
      // Held before binding (another customer's device) or at the SIM check.
      return o.contacted_at ? 2 : 1;
    case "IN_SERVICE":
      return STEPS.length;
    case "CANCELLED":
      return o.contacted_at ? 2 : 1;
  }
}

export function onboardingTimeline(o: Onboarding): TimelineStep[] {
  const at = position(o);
  const stopped = o.state === "ON_HOLD" || o.state === "FAILED";
  return STEPS.map((s, i) => ({
    ...s,
    status: i < at ? "done" : i > at ? "pending" : stopped ? "blocked" : o.state === "CANCELLED" ? "pending" : "current",
  }));
}

/** Splits an allowed-networks field into MCC+MNC codes (5 or 6 digits). */
export function parsePLMNs(text: string): { plmns: string[]; invalid: string[] } {
  const parts = text.split(/[\s,;]+/).filter(Boolean);
  return {
    plmns: parts.filter((p) => /^\d{5,6}$/.test(p)),
    invalid: parts.filter((p) => !/^\d{5,6}$/.test(p)),
  };
}
