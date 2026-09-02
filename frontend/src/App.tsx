import { Suspense, lazy, type ComponentType } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { Login } from "./screens/Login";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { AuthProvider } from "./auth/AuthContext";
import { useAuth } from "./auth/useAuth";
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

// Every screen is a lazy chunk (audit P2.4: one 750 kB bundle). The
// first paint only loads the shell plus the active screen; the rest
// arrive on demand. Screens are named exports, hence the .then() shim.
const screen = <T extends Record<string, ComponentType>>(load: () => Promise<T>, name: keyof T) =>
  lazy(() => load().then((m) => ({ default: m[name] })));

const SCREEN_COMPONENT: Record<Screen, ComponentType> = {
  dashboard: screen(() => import("./screens/Dashboard"), "Dashboard"),
  fleet: screen(() => import("./screens/DeviceFleet"), "DeviceFleet"),
  control: screen(() => import("./screens/FleetControl"), "FleetControl"),
  health: screen(() => import("./screens/FleetHealthScreen"), "FleetHealthScreen"),
  jobs: screen(() => import("./screens/Jobs"), "Jobs"),
  groups: screen(() => import("./screens/DeviceGroups"), "DeviceGroups"),
  templates: screen(() => import("./screens/ConfigTemplates"), "ConfigTemplates"),
  schedules: screen(() => import("./screens/ScheduledJobs"), "ScheduledJobs"),
  rollouts: screen(() => import("./screens/FirmwareRollouts"), "FirmwareRollouts"),
  policies: screen(() => import("./screens/Policies"), "Policies"),
  audit: screen(() => import("./screens/AuditLog"), "AuditLog"),
  operators: screen(() => import("./screens/Operators"), "Operators"),
  tenancy: screen(() => import("./screens/Tenancy"), "Tenancy"),
  reports: screen(() => import("./screens/Reports"), "Reports"),
  bss: screen(() => import("./screens/BSSIntegration"), "BSSIntegration"),
};

function AppShell() {
  // The active screen lives in the URL (audit P2.4: deep links) — /jobs,
  // /rollouts, /fleet?device=<id>, … — so views are linkable and the
  // browser's back button works. Unknown paths fall back to the fleet.
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const fromPath = pathname.split("/")[1] as Screen;
  const screen: Screen = SCREEN_COMPONENT[fromPath] ? fromPath : "fleet";
  const setScreen = (s: Screen) => navigate("/" + s);
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
          <select className="chip" aria-label="Console theme" value={theme} onChange={(e) => setTheme(e.target.value as Theme)} title="Console theme">
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
          <h1 className="sr-only">{NAV.find((item) => item.id === screen)?.label}</h1>
          <ErrorBoundary key={screen}>
            <Suspense fallback={<p className="dim">Loading…</p>}>
              <ActiveScreen />
            </Suspense>
          </ErrorBoundary>
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
