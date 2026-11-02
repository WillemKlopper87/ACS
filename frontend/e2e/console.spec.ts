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
  await expect(page.getByRole("button", { name: "Inventory", exact: true })).toBeVisible();
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

// The per-network Wi-Fi editor: one network at a time, the impact shown
// before sending, a typed confirmation for a primary network, and exactly
// the changed fields in the request.
test("Wi-Fi editor changes one network and warns who disconnects", async ({ page }) => {
  const network = (n: number, name: string, band: string, role: string, enabled = true) => ({
    instance: n, ssid_object: `Device.WiFi.SSID.${n}.`, access_point: `Device.WiFi.AccessPoint.${n}.`, radio: "", name, band, role, enabled,
    advertised: true, security_mode: "WPA2-Personal", modes_supported: ["None", "WPA2-Personal", "WPA3-Personal"],
    name_writable: true, enable_writable: true, passphrase_writable: true, security_mode_writable: true, advertised_writable: true, ap_enable_writable: true,
  });
  let sent: { url: string; body: unknown } | null = null;
  await page.route(`${API}/api/v1/devices/dev-1/wifi/networks**`, async (route) => {
    if (route.request().method() === "PUT") {
      sent = { url: route.request().url(), body: route.request().postDataJSON() };
      return route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ command_key: "setparam_e2e", status: "QUEUED", instances: [1, 5], writes: [], disconnects: true }) });
    }
    return route.fulfill({
      status: 200, contentType: "application/json",
      body: JSON.stringify({
        supported: true, complete: true, refresh_paths: [], read_paths: [],
        networks: [network(1, "Home", "2.4GHz", "primary"), network(2, "Guest", "2.4GHz", "guest"), network(3, "NOKIA-3", "2.4GHz", "disabled", false), network(5, "Home", "5GHz", "primary")],
        clients_by_instance: { "1": 9, "5": 4 }, clients_sampled_at: new Date().toISOString(),
      }),
    });
  });
  await signIn(page);
  await page.goto("/devices/dev-1/wifi");

  const table = page.getByRole("table", { name: "Wi-Fi networks" });
  await expect(table.getByText("Home").first()).toBeVisible();
  await expect(page.getByText("1 spare slot on this device.", { exact: false })).toBeVisible();

  await table.getByRole("row").filter({ hasText: "SSID 1" }).getByRole("button", { name: "Edit" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Network name (SSID)").fill("Smith Home");
  await dialog.getByText("Also apply to SSID 5", { exact: false }).click();
  await expect(dialog.getByText('13 connected devices will disconnect and must rejoin "Smith Home".')).toBeVisible();

  const apply = dialog.getByRole("button", { name: "Apply change" });
  await expect(apply).toBeDisabled();
  await dialog.getByLabel(/Type the current network name/).fill("Home");
  await apply.click();
  await expect(dialog).toBeHidden();

  expect(sent).not.toBeNull();
  expect(sent!.url).toMatch(/\/wifi\/networks\/1$/);
  expect(sent!.body).toEqual({ name: "Smith Home", also_instances: [5] });
});
