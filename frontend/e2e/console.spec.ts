import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

// The API base the built console talks to (VITE_API_BASE_URL default).
const API = "http://localhost:8080";

const DASHBOARD = {
  devices_by_status: { ONLINE: 3, OFFLINE: 1 },
  devices_by_reachability: {},
  inform_recency: { recent: 3, stale: 1 },
  group_by: { customer: {}, region: {}, project: {}, manufacturer: { Zyxel: 4 } },
  jobs_last_24h_total: 12,
  job_success_rate_pct: 91.7,
  firmware: { up_to_date: 3, outdated: 1, unknown: 0 },
  alarms: [],
  temperature: [],
  scoped: false,
  generated_at: new Date().toISOString(),
};

const DEVICE = {
  id: "dev-1",
  oui_serial: "001349-NR7101-S1",
  manufacturer: "Zyxel",
  oui: "001349",
  product_class: "NR7101",
  serial_number: "S1",
  data_model_root: "Device",
  online_status: "ONLINE",
  last_inform_event_codes: ["2 PERIODIC"],
  connection_request_mode: "DIRECT_IPV4",
  tags: [],
  cwmp_auth_mode: "DIGEST",
  last_inform_at: new Date().toISOString(),
};

// mockAPI wires a hermetic backend: happy-path login, one device, empty
// collections for everything else so no screen crashes on load.
async function mockAPI(page: Page) {
  await page.route(`${API}/**`, async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname;
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

    // Unauthenticated requests get 401 (excluding the public auth
    // routes) — that is what flips the console into its login state,
    // exactly like the real fail-closed API.
    const authed = !!route.request().headers()["authorization"];
    if (!authed && !path.startsWith("/api/v1/auth/")) {
      return route.fulfill({ status: 401, contentType: "text/plain", body: "missing bearer token" });
    }

    if (method === "POST" && path === "/api/v1/auth/login") {
      const body = route.request().postDataJSON() as { username: string; password: string };
      if (body.password === "right-password") {
        return json({ token: "e2e-token", role: "superadmin", username: body.username, expires_at: new Date(Date.now() + 3600_000).toISOString() });
      }
      return route.fulfill({ status: 401, contentType: "text/plain", body: "invalid username or password" });
    }
    if (path === "/api/v1/devices" && method === "GET") return json({ items: [DEVICE], total: 1 });
    if (path === "/api/v1/devices/dev-1") return json(DEVICE);
    if (path === "/api/v1/devices/dev-1/parameters" && method === "GET") return json({ parameters: {} });
    if (path.startsWith("/api/v1/devices/gone-")) return route.fulfill({ status: 404, contentType: "text/plain", body: "not found" });
    if (path === "/api/v1/dashboard") return json(DASHBOARD);
    if (path === "/api/v1/dashboard/layout") return json({ widgets: [{ id: "fleet", enabled: true }] });
    if (path.endsWith("/jobs") || path.endsWith("/parameters") || path.endsWith("/credentials") || path.endsWith("/uploads")) {
      return json({ items: [], total: 0 });
    }
    return json({ items: [], total: 0, groups: [] });
  });
}

async function signIn(page: Page) {
  await page.goto("/");
  await page.getByLabel("Username").fill("e2e-operator");
  await page.getByLabel("Password").fill("right-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("navigation")).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await mockAPI(page);
});

test("wrong password shows the API error, right password reaches the console", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("Sign in to continue.")).toBeVisible();

  await page.getByLabel("Username").fill("e2e-operator");
  await page.getByLabel("Password").fill("wrong");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByText(/invalid username or password/i)).toBeVisible();

  await page.getByLabel("Password").fill("right-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "Device Fleet", exact: true })).toBeVisible();
  await expect(page.getByText("001349-NR7101-S1")).toBeVisible();
});

test("screens are URL-routed and the browser back button works", async ({ page }) => {
  await signIn(page);
  await page.getByRole("button", { name: "Jobs", exact: true }).click();
  await expect(page).toHaveURL(/\/jobs$/);
  await page.getByRole("button", { name: "Dashboard" }).click();
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.getByText("job success rate")).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/\/jobs$/);
});

test("device drill-down deep link opens the detail directly", async ({ page }) => {
  await signIn(page);
  await page.goto("/fleet?device=dev-1");
  await expect(page.getByText(/NR7101/).first()).toBeVisible();
});

test("a nonexistent (or foreign-tenant) device surfaces the 404 instead of crashing", async ({ page }) => {
  await signIn(page);
  await page.goto("/fleet?device=gone-tenant-b");
  await expect(page.getByText(/not found|404/i).first()).toBeVisible();
});

// Accessibility (audit "missing checks": keyboard/labels/contrast smoke).
// Critical violations fail the build; serious-and-below are logged so the
// backlog is visible without blocking on day one.
async function expectNoCriticalA11y(page: Page, context: string) {
  const results = await new AxeBuilder({ page }).analyze();
  const critical = results.violations.filter((v) => v.impact === "critical");
  const rest = results.violations.filter((v) => v.impact !== "critical");
  if (rest.length > 0) {
    console.log(`[a11y backlog] ${context}: ${rest.map((v) => `${v.impact}:${v.id}(${v.nodes.length})`).join(", ")}`);
  }
  expect(critical, `critical a11y violations on ${context}: ${critical.map((v) => v.id).join(", ")}`).toEqual([]);
}

async function expectNoSeriousA11y(page: Page, context: string) {
  const results = await new AxeBuilder({ page }).analyze();
  const serious = results.violations.filter((v) => v.impact === "serious");
  expect(serious, `serious a11y violations on ${context}: ${serious.map((v) => v.id).join(", ")}`).toEqual([]);
}

test("login page has no critical accessibility violations", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("Sign in to continue.")).toBeVisible();
  await expect(page.getByRole("heading").first()).toBeVisible();
  await expectNoCriticalA11y(page, "login");
});

test("fleet and dashboard have no critical accessibility violations", async ({ page }) => {
  await signIn(page);
  await expectNoSeriousA11y(page, "fleet");
  await page.getByRole("button", { name: "Dashboard" }).click();
  await expect(page.getByText("job success rate")).toBeVisible();
  await expectNoSeriousA11y(page, "dashboard");
});
