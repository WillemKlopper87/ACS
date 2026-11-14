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

// Subscriber lookup: find a caller across the fleet by name, tell
// namesakes apart, open the device, and fill in a name the BSS has not
// sent (the BSS's own names are read-only).
test("subscriber lookup finds a caller and the device shows who it serves", async ({ page }) => {
  const sub = { device_id: "dev-1", account_id: "ACC-1001", subscriber_name: "Thandi Nkosi", vip: true, site: { street: "12 Rivonia Rd", suburb: "Sandton", city: "Johannesburg" },
    display_name: "Thandi Nkosi · ACC-1001 · Sandton · NR7101 …1-S1" };
  const hits = [
    { ...sub, oui_serial: DEVICE.oui_serial, manufacturer: "Zyxel", model: "NR7101", matched: ["name"] },
    { device_id: "dev-2", account_id: "ACC-2040", subscriber_name: "Thandi Nkosi", vip: false, site: { suburb: "Soweto", city: "Johannesburg" },
      display_name: "", oui_serial: "001349-NR7101-S9C3D4", manufacturer: "Zyxel", model: "NR7101", matched: ["name"] },
  ];
  let lookupQuery = "";
  let profileBody: unknown = null;
  await page.route(`${API}/api/v1/lookup**`, (route) => {
    lookupQuery = new URL(route.request().url()).search;
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: hits, count: hits.length }) });
  });
  await page.route(`${API}/api/v1/devices/dev-1`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...DEVICE, subscriber: sub }) }));
  await page.route(`${API}/api/v1/accounts/ACC-1001/profile`, (route) => {
    if (route.request().method() === "PUT") {
      profileBody = route.request().postDataJSON();
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ account_id: "ACC-1001", vip: false }) });
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ account_id: "ACC-1001", display_name: "Thandi Nkosi", name_source: "bss", vip: true, vip_source: "bss" }) });
  });

  await signIn(page);
  await page.getByLabel("Find a subscriber").fill("thandi");
  await page.getByRole("button", { name: "Find" }).click();
  const results = page.getByRole("list", { name: "Lookup results" });
  await expect(results.getByRole("listitem")).toHaveCount(2);
  await expect(results.getByText(/12 Rivonia Rd, Sandton/)).toBeVisible();
  await expect(results.getByText(/…C3D4/)).toBeVisible();
  expect(lookupQuery).toBe("?q=thandi");
  await expectNoSeriousA11y(page, "subscriber lookup");
  await page.screenshot({ path: "test-results/subscriber-lookup.png", fullPage: true });

  await results.getByRole("listitem").first().click();
  await expect(page.getByRole("heading", { name: /Thandi Nkosi/ })).toBeVisible();
  await expect(page.getByText(sub.display_name)).toBeVisible();
  await page.getByRole("button", { name: "Edit subscriber" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("Subscriber name")).toBeDisabled();
  await expect(dialog.getByText("Sent by the BSS; change it there.")).toBeVisible();
  await page.screenshot({ path: "test-results/subscriber-editor.png" });
  await dialog.getByLabel("On the VIP list").uncheck();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toBeHidden();
  expect(profileBody).toEqual({ vip: false });
});

// A sale in service can be ended (typed serial, archive on by default,
// reset off) or its device swapped; the console sends exactly that.
test("an in-service sale is ended or its device swapped from Onboarding", async ({ page }) => {
  const sale = {
    id: "ob-1", customer_id: null, device_id: "dev-1", expected_serial: "S1", state: "IN_SERVICE", state_reason: "", role: "gateway",
    commercial_model: "retail", source: "console", config_jobs: [], account_ref: "ACC-1001", created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(), in_service_at: new Date().toISOString(),
  };
  const sent: { path: string; body: unknown }[] = [];
  await page.route(`${API}/api/v1/onboardings**`, (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (req.method() === "POST" && path.endsWith("/end")) {
      sent.push({ path, body: req.postDataJSON() });
      return route.fulfill({ status: 200, contentType: "application/json",
        body: JSON.stringify({ onboarding_id: "ob-1", device_id: "dev-1", accounts: ["ACC-1001"], subscribers_archived: ["ACC-1001"], factory_reset: "" }) });
    }
    if (req.method() === "POST" && path.endsWith("/swap")) {
      sent.push({ path, body: req.postDataJSON() });
      return route.fulfill({ status: 200, contentType: "application/json",
        body: JSON.stringify({ ended_onboarding_id: "ob-1", replacement: { ...sale, id: "ob-2", expected_serial: "S2", state: "PLANNED" } }) });
    }
    if (path === "/api/v1/onboardings/unclaimed") return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [] }) });
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [sale] }) });
  });
  await signIn(page);
  await page.goto("/onboarding/completed");
  await page.getByRole("cell", { name: "S1" }).click();

  await page.getByRole("button", { name: "End service" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Reason").selectOption("rma");
  const confirm = dialog.getByRole("button", { name: "End service" });
  await expect(confirm).toBeDisabled();
  await dialog.getByLabel(/Type the serial/).fill("S1");
  await expectNoSeriousA11y(page, "end service dialog");
  await page.screenshot({ path: "test-results/end-service.png" });
  await confirm.click();
  await expect(dialog).toBeHidden();
  expect(sent[0]).toEqual({ path: "/api/v1/onboardings/ob-1/end", body: { reason: "rma", keep_subscriber: false, factory_reset: false } });

  await page.getByRole("cell", { name: "S1" }).click();
  await page.getByRole("button", { name: "Swap device" }).click();
  await page.getByRole("dialog").getByLabel("Replacement serial").fill("S2");
  await page.getByRole("dialog").getByRole("button", { name: "Swap device" }).click();
  expect(sent[1]).toEqual({ path: "/api/v1/onboardings/ob-1/swap", body: { reason: "rma", serial: "S2" } });
});

// Removing an account-device mapping from the BSS screen asks why (the
// API requires and audits a reason) and calls the unassign endpoint.
test("an admin removes an account-device mapping with a reason", async ({ page }) => {
  let removed = "";
  let items = [{ id: "m-1", account_id: "TEST-0001", device_id: "dev-1", oui_serial: DEVICE.oui_serial, status: "ACTIVE", role: "gateway" }];
  await page.route(`${API}/api/v1/bss/mappings**`, (route) => {
    const req = route.request();
    if (req.method() === "DELETE") {
      removed = new URL(req.url()).pathname + new URL(req.url()).search;
      items = [];
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items }) });
  });
  await signIn(page);
  await page.getByRole("button", { name: "BSS / NaaS" }).click();
  await page.getByRole("button", { name: `Remove ${DEVICE.oui_serial} from TEST-0001` }).click();
  await page.getByLabel("Reason for removing").selectOption("return");
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.getByText("No mappings yet.")).toBeVisible();
  expect(removed).toBe("/api/v1/bss/mappings/TEST-0001/gateway?reason=return");
});

// Ownership shows who the device serves with the name masked and the
// street left out; Show details fetches them (audited server-side). The
// page header's Back returns through the console's own history.
test("ownership masks the subscriber until Show, and Back returns to the previous page", async ({ page }) => {
  const sub = { device_id: "dev-1", account_id: "ACC-1001", subscriber_name: "Thandi Nkosi", vip: true, site: { suburb: "Sandton", city: "Johannesburg" },
    display_name: "Thandi Nkosi · ACC-1001 · Sandton · NR7101 …1-S1" };
  let reveals = 0;
  await page.route(`${API}/api/v1/devices/dev-1`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...DEVICE, subscriber: sub }) }));
  await page.route(`${API}/api/v1/devices/dev-1/subscriber/reveal`, (route) => {
    reveals++;
    return route.fulfill({ status: 200, contentType: "application/json",
      body: JSON.stringify({ ...sub, site: { name: "Home", street: "12 Rivonia Rd", suburb: "Sandton", city: "Johannesburg", postal_code: "2196" } }) });
  });
  await page.route(`${API}/api/v1/accounts/ACC-1001/profile`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ account_id: "ACC-1001", display_name: "Thandi Nkosi", name_source: "bss", vip: true }) }));

  await signIn(page);
  await page.goto("/devices/dev-1/ownership");
  await expect(page.getByRole("button", { name: "← Back" })).toHaveCount(0);
  const panel = page.locator(".panel").filter({ has: page.getByRole("heading", { name: /^Subscriber/ }) });
  await expect(panel.getByText("T*** N***")).toBeVisible();
  await expect(panel.getByText("Sandton, Johannesburg (street hidden)")).toBeVisible();
  await expect(panel.getByText("Rivonia")).toHaveCount(0);
  await panel.getByRole("button", { name: "Show details" }).click();
  await expect(panel.getByText("Home, 12 Rivonia Rd, Sandton, Johannesburg, 2196")).toBeVisible();
  await expect(panel.getByText(/^Thandi Nkosi · from the BSS$/)).toBeVisible();
  expect(reveals).toBe(1);
  await expectNoSeriousA11y(page, "ownership");
  await page.screenshot({ path: "test-results/ownership-revealed.png" });

  await page.getByRole("button", { name: "Inventory" }).click();
  await page.getByRole("button", { name: "Back to the previous page" }).click();
  await expect(page).toHaveURL(/\/devices\/dev-1\/ownership$/);
});

test("two-step sign-in asks for a code, survives a wrong one, and reaches the console", async ({ page }) => {
  // Registered after mockAPI, so these win for the login routes.
  await page.route(`${API}/api/v1/auth/login`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ mfa: "verify", mfa_token: "mfa-token", expires_at: new Date(Date.now() + 300_000).toISOString() }) }),
  );
  await page.route(`${API}/api/v1/auth/login/mfa`, (route) => {
    const body = route.request().postDataJSON() as { mfa_token: string; code: string };
    if (body.mfa_token === "mfa-token" && body.code === "123456") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ token: "e2e-token", role: "superadmin", expires_at: new Date(Date.now() + 3600_000).toISOString() }) });
    }
    return route.fulfill({ status: 401, contentType: "text/plain", body: "that code is not valid — check the app and try the next one" });
  });

  await page.goto("/");
  await page.getByLabel("Username").fill("e2e-operator");
  await page.getByLabel("Password").fill("right-password");
  await page.getByRole("button", { name: "Sign in" }).click();

  // The password alone does not open the console.
  await expect(page.getByText("six-digit code")).toBeVisible();
  await expect(page.getByRole("navigation")).toHaveCount(0);

  await page.getByLabel("Code").fill("000000");
  await page.getByRole("button", { name: "Verify" }).click();
  await expect(page.getByText("that code is not valid")).toBeVisible();
  // A wrong code is not a sign-out: the prompt is still there.
  await expect(page.getByRole("button", { name: "Verify" })).toBeVisible();

  await page.getByLabel("Code").fill("123456");
  await page.getByRole("button", { name: "Verify" }).click();
  await expect(page.getByRole("navigation")).toBeVisible();

  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations.filter((v) => v.impact === "critical")).toEqual([]);
});

test("a destructive action asks in the console's own dialog, and Escape means no", async ({ page }) => {
  let deleted = false;
  await page.route(`${API}/api/v1/device-groups`, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [{ id: "g-1", name: "Pilot", member_count: 2, created_at: new Date().toISOString() }] }) }),
  );
  await page.route(`${API}/api/v1/device-groups/g-1`, (route) => {
    if (route.request().method() === "DELETE") deleted = true;
    return route.fulfill({ status: 204, body: "" });
  });
  await signIn(page);
  await page.goto("/groups");
  await page.getByRole("button", { name: "Delete" }).click();

  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toContainText('Delete group "Pilot" (2 members)?');
  // Focus starts on the safe choice.
  await expect(dialog.getByRole("button", { name: "Keep as it is" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  expect(deleted).toBe(false);

  await page.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Delete" }).click();
  await expect(page.getByRole("alertdialog")).toHaveCount(0);
  expect(deleted).toBe(true);
});
