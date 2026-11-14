import { Suspense, lazy, type ComponentType } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { Login } from "./screens/Login";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { AuthProvider } from "./auth/AuthContext";
import { useAuth } from "./auth/useAuth";
import { canAdmin } from "./auth/roles";
import { ToastHost } from "./components/Toast";
import { ConfirmHost } from "./components/ConfirmHost";
import { GlobalSearch } from "./components/GlobalSearch";
import { areaOf, screenTitle, visibleAreas, type Screen } from "./lib/nav";
import { useApiReachable } from "./lib/apiHealth";
import { useTheme, type Theme } from "./theme/useTheme";
import { grafanaLink, openGatedGrafana } from "./lib/grafana";
import { api } from "./api/client";
import { toast } from "./lib/toast";

// Shown next to the brand so a console pointed at production is never
// mistaken for a scratch one. Deliberately absent rather than defaulted:
// this was hardcoded to "dev" and shipped that way in every build, which
// is worse than no badge at all. Set VITE_ENV_LABEL at build time.
const ENV_LABEL = import.meta.env.VITE_ENV_LABEL as string | undefined;

// Grafana opens on its own origin (lib/grafana.ts); absent when the build
// has no VITE_GRAFANA_URL.
const GRAFANA = grafanaLink(import.meta.env.VITE_GRAFANA_URL as string | undefined, import.meta.env.VITE_API_BASE_URL as string | undefined);
const GRAFANA_GATED = import.meta.env.VITE_GRAFANA_GATE === "1";

async function openGrafana() {
  const problem = await openGatedGrafana(api.grafanaTicket, () => window.open("about:blank", "_blank"));
  if (problem) toast(problem, "error");
}


const THEME_LABEL: Record<Theme, string> = { dark: "Dark", light: "Light", unfold: "Unfold", fluent: "Fluent 2" };
const SUPPORTED_THEMES: Theme[] = ["dark", "light"];


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
  devices: screen(() => import("./screens/DeviceWorkspace"), "DeviceWorkspace"),
  onboarding: screen(() => import("./screens/Onboarding"), "Onboarding"),
  control: screen(() => import("./screens/FleetControl"), "FleetControl"),
  operations: screen(() => import("./screens/BulkOperations"), "BulkOperations"),
  health: screen(() => import("./screens/FleetHealthScreen"), "FleetHealthScreen"),
  jobs: screen(() => import("./screens/Jobs"), "Jobs"),
  groups: screen(() => import("./screens/DeviceGroups"), "DeviceGroups"),
  templates: screen(() => import("./screens/ConfigTemplates"), "ConfigTemplates"),
  schedules: screen(() => import("./screens/ScheduledJobs"), "ScheduledJobs"),
  rollouts: screen(() => import("./screens/FirmwareRollouts"), "FirmwareRollouts"),
  policies: screen(() => import("./screens/Policies"), "Policies"),
  audit: screen(() => import("./screens/AuditLog"), "AuditLog"),
  captures: screen(() => import("./screens/CaptureSessions"), "CaptureSessions"),
  operators: screen(() => import("./screens/Operators"), "Operators"),
  tenancy: screen(() => import("./screens/Tenancy"), "Tenancy"),
  reports: screen(() => import("./screens/Reports"), "Reports"),
  bss: screen(() => import("./screens/BSSIntegration"), "BSSIntegration"),
  help: screen(() => import("./screens/Help"), "Help"),
  alerting: screen(() => import("./screens/Alerting"), "Alerting"),
  account: screen(() => import("./screens/Account"), "Account"),
};

function AppShell() {
  // The active screen lives in the URL (audit P2.4: deep links) — /jobs,
  // /rollouts, /fleet?device=<id>, … — so views are linkable and the
  // browser's back button works. Unknown paths fall back to the fleet.
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const fromPath = pathname.split("/")[1] as Screen;
  const screen: Screen = SCREEN_COMPONENT[fromPath] ? fromPath : "fleet";
  // The sidebar lists areas; the screens of the current area are tabs
  // above the content (lib/nav.ts).
  const area = areaOf(screen);
  // A device's own pages sit under Inventory.
  const tabScreen: Screen = screen === "devices" ? "fleet" : screen;
  const setScreen = (s: Screen) => navigate("/" + s);

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

        <nav className="sidebar-nav" aria-label="Main">
          {visibleAreas(admin).map((item) => (
            <button
              key={item.id}
              className={area?.id === item.id ? "active" : ""}
              aria-current={area?.id === item.id ? "true" : undefined}
              onClick={() => setScreen(item.screens[0].id)}
            >
              {item.label}
            </button>
          ))}
          {GRAFANA && GRAFANA_GATED && (
            <button type="button" onClick={() => void openGrafana()} title="Opens Grafana in a new tab after checking your console sign-in">
              Grafana <span aria-hidden="true">↗</span>
              <span className="sr-only">(opens in a new tab)</span>
            </button>
          )}
          {GRAFANA && !GRAFANA_GATED && (
            <a
              className="nav-external"
              href={GRAFANA.href}
              target="_blank"
              rel="noopener noreferrer"
              title={GRAFANA.tunnelHint ? `Grafana is only on the server's loopback. Open an SSH tunnel first: ${GRAFANA.tunnelHint}` : "Opens Grafana in a new tab"}
            >
              Grafana <span aria-hidden="true">↗</span>
              <span className="sr-only">(opens in a new tab)</span>
            </a>
          )}
        </nav>

        <div className="sidebar-footer">
          <select className="chip" aria-label="Console theme" value={theme} onChange={(e) => setTheme(e.target.value as Theme)} title="Console theme">
            {/* Dark and Light are the themes new screens are designed and
                checked in. The other two stay selectable, but are not kept
                up to date (docs/CONSOLE-REDESIGN-AND-MFA.md, step G). */}
            <optgroup label="Supported">
              {themes.filter((t) => SUPPORTED_THEMES.includes(t)).map((t) => (
                <option key={t} value={t}>
                  {THEME_LABEL[t]}
                </option>
              ))}
            </optgroup>
            <optgroup label="Not maintained">
              {themes.filter((t) => !SUPPORTED_THEMES.includes(t)).map((t) => (
                <option key={t} value={t}>
                  {THEME_LABEL[t]}
                </option>
              ))}
            </optgroup>
          </select>
          {token && (
            <div className="conn-indicator" style={{ justifyContent: "space-between" }}>
              <button className="account-link" onClick={() => setScreen("account")} title="Your account and two-step sign-in" aria-current={screen === "account" ? "page" : undefined}>
                {username} <span style={{ color: "var(--ink-faint)" }}>({role})</span>
              </button>
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
          <div className="page-title-row">
            {/* Back through the console's own history (React Router keeps
                its position in history.state.idx); hidden on the first
                page, where Back would leave the console. */}
            {(window.history.state?.idx ?? 0) > 0 && (
              <button className="btn sm" onClick={() => navigate(-1)} aria-label="Back to the previous page">
                ← Back
              </button>
            )}
            <h1 className="page-title">{screenTitle(screen)}</h1>
            <GlobalSearch />
          </div>
          {area && area.screens.length > 1 && (
            <nav className="area-tabs" aria-label={`${area.label} sections`}>
              {area.screens.map((item) => (
                <Link key={item.id} to={"/" + item.id} className={item.id === tabScreen ? "on" : ""} aria-current={item.id === tabScreen ? "page" : undefined}>
                  {item.label}
                </Link>
              ))}
            </nav>
          )}
          <ErrorBoundary key={screen}>
            <Suspense fallback={<p className="dim">Loading…</p>}>
              <ActiveScreen />
            </Suspense>
          </ErrorBoundary>
        </main>
      </div>
      <ToastHost />
      <ConfirmHost />
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
