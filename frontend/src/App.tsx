import { useState, type ComponentType } from "react";
import { DeviceFleet } from "./screens/DeviceFleet";
import { FleetControl } from "./screens/FleetControl";
import { Jobs } from "./screens/Jobs";
import { Login } from "./screens/Login";
import { DeviceGroups } from "./screens/DeviceGroups";
import { ScheduledJobs } from "./screens/ScheduledJobs";
import { Policies } from "./screens/Policies";
import { FirmwareRollouts } from "./screens/FirmwareRollouts";
import { AuditLog } from "./screens/AuditLog";
import { FleetHealthScreen } from "./screens/FleetHealthScreen";
import { Operators } from "./screens/Operators";
import { ConfigTemplates } from "./screens/ConfigTemplates";
import { Tenancy } from "./screens/Tenancy";
import { Dashboard } from "./screens/Dashboard";
import { Reports } from "./screens/Reports";
import { BSSIntegration } from "./screens/BSSIntegration";
import { AuthProvider, useAuth } from "./auth/AuthContext";
import { canAdmin } from "./auth/roles";
import { ToastHost } from "./components/Toast";
import { useTheme, type Theme } from "./theme/useTheme";

type Screen = "dashboard" | "fleet" | "control" | "health" | "jobs" | "groups" | "schedules" | "policies" | "rollouts" | "audit" | "operators" | "templates" | "tenancy" | "reports" | "bss";

const THEME_LABEL: Record<Theme, string> = { dark: "Dark", light: "Light", unfold: "Unfold", fluent: "Fluent 2" };

// Data-driven nav — the sidebar renders this list instead of one JSX
// button per screen, so adding a screen is a one-line change here rather
// than a repeated block (build plan's own convention, applied to the nav
// the same way every screen list elsewhere in this app already is).
const NAV: { id: Screen; label: string; adminOnly?: boolean }[] = [
  { id: "dashboard", label: "Dashboard" },
  { id: "fleet", label: "Device Fleet" },
  { id: "control", label: "Fleet Control" },
  { id: "health", label: "Fleet Health" },
  { id: "jobs", label: "Jobs" },
  { id: "groups", label: "Groups" },
  { id: "templates", label: "Templates" },
  { id: "schedules", label: "Scheduled Jobs" },
  { id: "rollouts", label: "Rollouts" },
  { id: "policies", label: "Policies" },
  { id: "audit", label: "Audit Log" },
  { id: "operators", label: "Operators", adminOnly: true },
  { id: "tenancy", label: "Tenancy", adminOnly: true },
  { id: "reports", label: "Reports" },
  { id: "bss", label: "BSS Integration", adminOnly: true },
];

const SCREEN_COMPONENT: Record<Screen, ComponentType> = {
  dashboard: Dashboard,
  fleet: DeviceFleet,
  control: FleetControl,
  health: FleetHealthScreen,
  jobs: Jobs,
  groups: DeviceGroups,
  templates: ConfigTemplates,
  schedules: ScheduledJobs,
  rollouts: FirmwareRollouts,
  policies: Policies,
  audit: AuditLog,
  operators: Operators,
  tenancy: Tenancy,
  reports: Reports,
  bss: BSSIntegration,
};

function AppShell() {
  const [screen, setScreen] = useState<Screen>("fleet");
  const { token, authRequired, username, role, logout } = useAuth();
  const admin = canAdmin(role);
  const { theme, setTheme, themes } = useTheme();

  if (authRequired && !token) {
    return <Login />;
  }

  const ActiveScreen = SCREEN_COMPONENT[screen];

  return (
    <div className="shell shell-sidebar">
      <aside className="sidebar">
        <div className="brand">
          <span className="dot">●</span> ACS<span style={{ color: "var(--ink-faint)" }}>/</span>console
          <span className="env">dev</span>
        </div>

        <nav className="sidebar-nav">
          {NAV.filter((item) => !item.adminOnly || admin).map((item) => (
            <button key={item.id} className={screen === item.id ? "active" : ""} onClick={() => setScreen(item.id)}>
              {item.label}
            </button>
          ))}
        </nav>

        <div className="sidebar-footer">
          <select className="chip" value={theme} onChange={(e) => setTheme(e.target.value as Theme)} title="Console theme">
            {themes.map((t) => (
              <option key={t} value={t}>
                {THEME_LABEL[t]}
              </option>
            ))}
          </select>
          {token && (
            <div className="conn-indicator" style={{ justifyContent: "space-between" }}>
              <span>
                {username} <span style={{ color: "var(--ink-faint)" }}>({role})</span>
              </span>
              <button
                onClick={logout}
                style={{
                  background: "none",
                  border: "1px solid var(--border)",
                  borderRadius: 4,
                  color: "var(--ink-dim)",
                  padding: "0.15em 0.5em",
                  cursor: "pointer",
                  fontSize: "0.72rem",
                }}
              >
                Sign out
              </button>
            </div>
          )}
          <div className="conn-indicator">
            <span className="dot" /> {import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8080"}
          </div>
        </div>
      </aside>

      <div className="shell-main">
        <main className="view">
          <ActiveScreen />
        </main>
      </div>
      <ToastHost />
    </div>
  );
}

export default function App() {
  return (
    <AuthProvider>
      <AppShell />
    </AuthProvider>
  );
}
