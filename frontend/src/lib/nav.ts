// The console's navigation: a few areas in the sidebar, each holding the
// screens that belong together as tabs (docs/CONSOLE-REDESIGN-AND-MFA.md,
// step D). Every screen keeps the URL it always had (/jobs, /rollouts, …):
// an area is only a grouping, so bookmarks and links keep working.

export type Screen =
  | "dashboard" | "fleet" | "devices" | "onboarding" | "control" | "operations" | "health" | "jobs" | "groups"
  | "schedules" | "policies" | "rollouts" | "audit" | "captures" | "operators" | "templates" | "tenancy"
  | "reports" | "bss" | "help" | "alerting" | "account";

export interface NavScreen {
  id: Screen;
  label: string;
}

export interface NavArea {
  id: string;
  label: string;
  // Hidden from operators who are not superadmins; the API enforces it.
  adminOnly?: boolean;
  // In the order an operator reaches for them. The first is where the
  // sidebar entry lands.
  screens: NavScreen[];
}

export const AREAS: NavArea[] = [
  {
    id: "overview",
    label: "Overview",
    screens: [
      { id: "dashboard", label: "Dashboard" },
      { id: "health", label: "Fleet health" },
      { id: "alerting", label: "Alerting" },
    ],
  },
  {
    id: "devices",
    label: "Devices",
    screens: [
      { id: "fleet", label: "Inventory" },
      { id: "onboarding", label: "Onboarding" },
      { id: "groups", label: "Groups" },
      { id: "control", label: "Bulk actions" },
    ],
  },
  {
    id: "activity",
    label: "Activity",
    screens: [
      { id: "jobs", label: "Jobs" },
      { id: "operations", label: "Bulk operations" },
      { id: "schedules", label: "Scheduled jobs" },
    ],
  },
  {
    id: "configuration",
    label: "Configuration",
    screens: [
      { id: "templates", label: "Templates" },
      { id: "policies", label: "Policies" },
      { id: "rollouts", label: "Rollouts" },
    ],
  },
  {
    id: "records",
    label: "Records & tools",
    screens: [
      { id: "audit", label: "Audit log" },
      { id: "reports", label: "Reports" },
      { id: "captures", label: "Session captures" },
    ],
  },
  {
    id: "administration",
    label: "Administration",
    adminOnly: true,
    screens: [
      { id: "operators", label: "Operators" },
      { id: "tenancy", label: "Tenancy" },
      { id: "bss", label: "BSS / NaaS" },
    ],
  },
  { id: "help", label: "Help", screens: [{ id: "help", label: "Help" }] },
];

// Screens that are reached from another screen, not from the navigation.
const TITLES: Partial<Record<Screen, string>> = { devices: "Device", account: "Account" };

export function visibleAreas(admin: boolean): NavArea[] {
  return AREAS.filter((area) => !area.adminOnly || admin);
}

// The area a screen sits in. A device's own pages belong with Devices.
export function areaOf(screen: Screen): NavArea | undefined {
  const lookup = screen === "devices" ? "fleet" : screen;
  return AREAS.find((area) => area.screens.some((s) => s.id === lookup));
}

export function screenTitle(screen: Screen): string {
  return TITLES[screen] ?? AREAS.flatMap((a) => a.screens).find((s) => s.id === screen)?.label ?? "";
}
