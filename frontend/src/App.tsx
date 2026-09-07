import { Suspense, lazy, type ComponentType, useState} from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { Login } from "./screens/Login";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { AuthProvider } from "./auth/AuthContext";
import { useAuth } from "./auth/useAuth";
import { canAdmin } from "./auth/roles";
import { ToastHost } from "./components/Toast";
import { useApiReachable } from "./lib/apiHealth";
import { useTheme, type Theme } from "./theme/useTheme";

// Shown next to the brand so a console pointed at production is never
// mistaken for a scratch one. Deliberately absent rather than defaulted:
// this was hardcoded to "dev" and shipped that way in every build, which
// is worse than no badge at all. Set VITE_ENV_LABEL at build time.
const ENV_LABEL = import.meta.env.VITE_ENV_LABEL as string | undefined;

type Screen = "dashboard" | "fleet" | "control" | "health" | "jobs" | "groups" | "schedules" | "policies" | "rollouts" | "audit" | "operators" | "templates" | "tenancy" | "reports" | "bss" | "help";

const THEME_LABEL: Record<Theme, string> = { dark: "Dark", light: "Light", unfold: "Unfold", fluent: "Fluent 2" };

// Data-driven nav — the sidebar renders this list instead of one JSX
// button per screen, so adding a screen is a one-line change here rather
// than a repeated block (build plan's own convention, applied to the nav
// the same way every screen list elsewhere in this app already is).
// Grouped rather than one flat list of fifteen: the sections answer "what
// am I trying to do" (watch the fleet / act on devices / change how devices
// are configured / administer the platform / look something up), which is
// how an operator arrives at the console. Order within a group runs from
// the screen you open most to the one you open least.
const NAV: { id: Screen; label: string; adminOnly?: boolean; group: string }[] = [
  { group: "Monitor", id: "dashboard", label: "Dashboard" },
  { group: "Monitor", id: "health", label: "Fleet Health" },
  { group: "Monitor", id: "jobs", label: "Jobs" },

  { group: "Devices", id: "fleet", label: "Device Fleet" },
  { group: "Devices", id: "control", label: "Fleet Control" },
  { group: "Devices", id: "groups", label: "Groups" },

  { group: "Provisioning", id: "templates", label: "Templates" },
  { group: "Provisioning", id: "policies", label: "Policies" },
  { group: "Provisioning", id: "schedules", label: "Scheduled Jobs" },
  { group: "Provisioning", id: "rollouts", label: "Rollouts" },

  { group: "Administration", id: "operators", label: "Operators", adminOnly: true },
  { group: "Administration", id: "tenancy", label: "Tenancy", adminOnly: true },
  { group: "Administration", id: "bss", label: "BSS Integration", adminOnly: true },

  { group: "Records", id: "audit", label: "Audit Log" },
  { group: "Records", id: "reports", label: "Reports" },
  { group: "Records", id: "help", label: "Help" },
];

// Preserves NAV order without hardcoding the section list in two places, so
// adding a screen stays the one-line change it was before.
const NAV_COLLAPSED_KEY = "acs.nav.collapsed";

const NAV_GROUPS = NAV.reduce<string[]>((acc, item) => {
  if (!acc.includes(item.group)) acc.push(item.group);
  return acc;
}, []);

// Every screen is a lazy chunk (audit P2.4: one 750 kB bundle). The
// first paint only loads the shell plus the active screen; the rest
// arrive on demand. Screens are named exports, hence the .then() shim.
// Constrains only the export being loaded, not every export in the module:
// `Record<string, ComponentType>` forced each screen file to export nothing
// but zero-prop components, so a screen that also exports a small helper
// component (BSSIntegration's WebhookRow, extracted for its regression
// test) failed to typecheck for reasons that had nothing to do with the
// screen itself.
const screen = <K extends string, T extends Record<K, ComponentType>>(
  load: () => Promise<T>,
  name: K,
) => lazy(() => load().then((m) => ({ default: m[name] })).catch(onChunkLoadError));

// Screens are content-hashed chunks fetched on demand, so a deploy that
// lands while someone has the console open leaves their tab asking for a
// hash that no longer exists. That is not an application error and an
// error boundary is the wrong response to it: the fix is simply to load
// the new bundle.
//
// Reloading is guarded by a sessionStorage flag so a genuinely broken
// chunk cannot put the tab in a refresh loop — the second failure in a
// session falls through to the error boundary, where it belongs.
const RELOADED_KEY = "acs.chunk.reloaded";

function onChunkLoadError(err: unknown): never {
  let alreadyTried = false;
  try {
    alreadyTried = sessionStorage.getItem(RELOADED_KEY) === "1";
    if (!alreadyTried) sessionStorage.setItem(RELOADED_KEY, "1");
  } catch {
    // Private windows and blocked site data: fall through to the error
    // boundary rather than risking a loop we cannot detect.
    alreadyTried = true;
  }
  if (!alreadyTried) window.location.reload();
  throw err;
}

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
  help: screen(() => import("./screens/Help"), "Help"),
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

  // Which nav sections are collapsed. Persisted per browser so an operator
  // who only ever uses two sections keeps their layout between visits; a
  // read failure (private window, blocked site data) just means everything
  // starts expanded, which is the safe default.
  const [collapsed, setCollapsed] = useState<string[]>(() => {
    try {
      const raw = localStorage.getItem(NAV_COLLAPSED_KEY);
      return raw ? (JSON.parse(raw) as string[]) : [];
    } catch {
      return [];
    }
  });
  const toggleGroup = (group: string) =>
    setCollapsed((prev) => {
      const next = prev.includes(group) ? prev.filter((g) => g !== group) : [...prev, group];
      try {
        localStorage.setItem(NAV_COLLAPSED_KEY, JSON.stringify(next));
      } catch {
        // Persistence is a convenience, never a requirement.
      }
      return next;
    });
  const { token, authRequired, username, role, logout } = useAuth();
  const admin = canAdmin(role);
  const { theme, setTheme, themes } = useTheme();
  const apiReachable = useApiReachable();

  if (authRequired && !token) {
    return <Login />;
  }

  const ActiveScreen = SCREEN_COMPONENT[screen];

  return (
    <div className="shell shell-sidebar">
      <aside className="sidebar">
        <div className="brand">
          <span className="dot">●</span> ACS<span style={{ color: "var(--ink-faint)" }}>/</span>console
          {ENV_LABEL && <span className="env">{ENV_LABEL}</span>}
        </div>

        <nav className="sidebar-nav">
          {NAV_GROUPS.map((group) => {
            const items = NAV.filter((item) => item.group === group && (!item.adminOnly || admin));
            // A non-admin sees no Administration items at all, so the
            // heading would otherwise sit above an empty section.
            if (items.length === 0) return null;
            // The group holding the current screen is always shown, even if
            // it was collapsed earlier — collapsing a section should never
            // hide where you actually are.
            const holdsActive = items.some((item) => item.id === screen);
            const open = holdsActive || !collapsed.includes(group);
            return (
              <div key={group} className="nav-group">
                <button
                  type="button"
                  className="nav-group-label"
                  aria-expanded={open}
                  aria-controls={`nav-group-${group}`}
                  onClick={() => toggleGroup(group)}
                >
                  <span className="nav-caret" aria-hidden="true">{open ? "▾" : "▸"}</span>
                  {group}
                </button>
                <div id={`nav-group-${group}`} role="group" aria-label={group} hidden={!open}>
                  {items.map((item) => (
                    <button
                      key={item.id}
                      className={screen === item.id ? "active" : ""}
                      aria-current={screen === item.id ? "page" : undefined}
                      onClick={() => setScreen(item.id)}
                    >
                      {item.label}
                    </button>
                  ))}
                </div>
              </div>
            );
          })}
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
              <button className="btn ghost sm" onClick={logout}>
                Sign out
              </button>
            </div>
          )}
          <div
            className={`conn-indicator${apiReachable ? "" : " down"}`}
            title={apiReachable ? "Last API call reached the server" : "The last API call did not reach the server"}
          >
            <span className="dot" /> {import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8080"}
            <span className="sr-only">{apiReachable ? " — reachable" : " — unreachable"}</span>
          </div>
        </div>
      </aside>

      <div className="shell-main">
        <main className="view">
          {/* Visible, not sr-only: screens opened straight into a toolbar
              with nothing naming them but the sidebar's highlighted row,
              which is gone the moment the sidebar collapses on a narrow
              viewport. */}
          <h1 className="page-title">{NAV.find((item) => item.id === screen)?.label}</h1>
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
