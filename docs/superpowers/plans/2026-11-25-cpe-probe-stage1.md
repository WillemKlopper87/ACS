# CPE Probe (telemetry stage 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator read chosen parameters from a device on demand from the Parameters page, with a one-click FastMile telemetry preset, and record what the lab FastMile returns.

**Architecture:** A frontend-only change. The existing `POST /api/v1/devices/{id}/parameters/get` (permission `devices.write`, queues a `GET_PARAMETER` job answered at the device's next check-in, result lands in the parameter cache) is reused unchanged. A pure module builds the preset from the device's discovered parameter names; a panel on the Parameters page sends the read. A short document holds the procedure and, once the lab run is done, the findings.

**Tech Stack:** React 19, TypeScript 6, Vitest and Testing Library (run under Node 22), the console's existing `api` client and `toast`.

**Spec:** `docs/superpowers/specs/2026-11-25-cpe-telemetry-design.md`, section "Phase 1: probe". Stages 2a (signal trend) and 2b (link changes and location) are separate plans, written after this one ships.

## Global Constraints

- No backend change, no migration, no new dependency, no change to `backend/openapi.yaml`.
- Permission: the panel is usable only when `canWrite(role)` is true (the `writable` value `DeviceDetail` already computes); otherwise it is shown disabled with a reason.
- A path that is not in the device's discovered names faults the whole `GET_PARAMETER`, so when names are known every path sent must be in them.
- Location coordinates are read only when the operator ticks an explicit box (default off). The findings document records the coordinates' *format* and never a position.
- Checks run with `scripts/ci-local.sh --only spec,frontend` (Node 22 for the unit tests). GitHub Actions is not available.
- Console copy is plain language; the existing `toast(message, "error")` shape is used for failures.

## Review Focus

- Discovered names not loaded (`null`) or empty: the preset yields nothing and the panel says the model has not been discovered; it never sends an empty read.
- A typed path the device does not have (or one from another vendor or root): blocked with the path named, so one typo cannot fault the whole read.
- An object path ending in `.` (for example `Device.`) or a very long list: blocked, because it would pull thousands of values in one session.
- A read-only role: the panel is disabled with a stated reason and never calls the API.
- Coordinates: absent from the preset unless the box is ticked, and the box resets to unticked after a read is sent.
- A double click on Read: exactly one job is queued.

## File Structure

- Create `frontend/src/lib/telemetryProbe.ts` and `telemetryProbe.test.ts`: the preset and the path checks, pure functions.
- Create `frontend/src/components/ReadParametersPanel.tsx` and `ReadParametersPanel.test.tsx`: the panel.
- Modify `frontend/src/screens/DeviceDetail.tsx` (Parameters section only) and `DeviceDetail.workspace.test.tsx`.
- Create `docs/CPE-PROBE.md`: procedure and findings.

---

### Task 1: The probe path list

**Files:**
- Create: `frontend/src/lib/telemetryProbe.ts`
- Test: `frontend/src/lib/telemetryProbe.test.ts`

**Interfaces:**
- Produces:
  - `export const MAX_PROBE_PATHS = 60`
  - `export function probePaths(names: Record<string, boolean> | null, opts: { includeCoordinates: boolean }): string[]`: the preset, restricted to discovered names, sorted, no duplicates; `[]` when `names` is `null` or empty.
  - `export function parsePaths(text: string): string[]`: one path per line, trimmed, blank lines dropped, duplicates removed, order kept.
  - `export function checkPaths(paths: string[], names: Record<string, boolean> | null): { unknown: string[]; objectPaths: string[]; tooMany: boolean }`: `unknown` is the paths absent from `names` (always `[]` when `names` is `null`), `objectPaths` those ending in `.`, `tooMany` true above `MAX_PROBE_PATHS`.

- [ ] **Step 1: Write the failing tests** in `telemetryProbe.test.ts`, with a `fastmile` names fixture built from the real lab list (TR-181 root `Device.`):

```ts
const fastmile: Record<string, boolean> = Object.fromEntries([
  "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.RSRPCurrent",
  "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.SNRCurrent",
  "Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.Band",
  "Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.RSRPCurrent",
  "Device.Cellular.Interface.1.X_ALU-COM_AttachedLteCellStat.PhysicalCellID",
  "Device.Cellular.Interface.1.LastChange", "Device.Cellular.Interface.1.NetworkInUse",
  "Device.Cellular.RoamingStatus", "Device.IP.Interface.1.LastChange", "Device.IP.Interface.2.LastChange",
  "Device.Ethernet.Link.1.LastChange", "Device.Ethernet.Link.2.LastChange", "Device.DeviceInfo.UpTime",
  "Device.DeviceInfo.Location.1.Source", "Device.DeviceInfo.Location.1.AcquiredTime",
  "Device.DeviceInfo.Location.1.X_ALU-COM_TimeStamp", "Device.DeviceInfo.Location.1.X_ALU-COM_Latitude",
  "Device.DeviceInfo.Location.1.X_ALU-COM_Longitude", "Device.DeviceInfo.Location.1.X_ALU-COM_Altitude",
  "Device.WiFi.Radio.1.Enable",
].map((n) => [n, false]));
```

Tests (names and assertions):
  - `probePaths(fastmile, {includeCoordinates:false})` contains the 5G and LTE signal leaves, the three `LastChange` groups, `UpTime`, `NetworkInUse`, `RoamingStatus`, `Location.1.Source`, `AcquiredTime` and `X_ALU-COM_TimeStamp`; does **not** contain any `_Latitude`, `_Longitude`, `_Altitude` or `Device.WiFi.Radio.1.Enable`.
  - with `includeCoordinates:true` it adds exactly the three coordinate leaves.
  - a path in the preset but missing from the names is not returned (delete `Device.IP.Interface.2.LastChange` from a copy and assert it is absent).
  - `probePaths(null, …)` and `probePaths({}, …)` return `[]`; a names object with only `InternetGatewayDevice.` paths returns `[]`.
  - the result is sorted and has no duplicates.
  - `parsePaths("  a \n\nb\na\n")` equals `["a","b"]`.
  - `checkPaths(["Device.X","Device.","Device.DeviceInfo.UpTime"], fastmile)` gives `unknown: ["Device.X","Device."]`, `objectPaths: ["Device."]`, `tooMany: false`; with `names = null`, `unknown` is `[]`; 61 paths give `tooMany: true`, 60 give `false`.

- [ ] **Step 2: Run to verify failure.** Run: `cd frontend && npx vitest run src/lib/telemetryProbe.test.ts` (under Node 22, as `scripts/ci-local.sh` does). Expected: FAIL, module not found.

- [ ] **Step 3: Implement the module** in `telemetryProbe.ts`. Build `probePaths` by filtering the discovered names through these patterns (always on): Nokia signal leaves `^Device\.Cellular\.Interface\.\d+\.X_[A-Za-z0-9-]+_Attached(5G|Lte)CellStat\.(RSRPCurrent|RSRQCurrent|SNRCurrent|RSSICurrent|Band|PhysicalCellID|Cellid|DownlinkEarfcn|Downlink-NR-ARFCN)$`; link timers `^Device\.(Cellular\.Interface|IP\.Interface|Ethernet\.Interface|Ethernet\.Link)\.\d+\.LastChange$`; `^Device\.DeviceInfo\.UpTime$`; `^Device\.Cellular\.Interface\.\d+\.NetworkInUse$`; `^Device\.Cellular\.RoamingStatus$`; location metadata `^Device\.DeviceInfo\.Location\.1\.(Source|AcquiredTime|X_[A-Za-z0-9-]+_TimeStamp)$`; and, only when `includeCoordinates`, `^Device\.DeviceInfo\.Location\.1\.X_[A-Za-z0-9-]+_(Latitude|Longitude|Altitude)$`. Sort with `localeCompare`.

- [ ] **Step 4: Run to verify they pass**, same command. Expected: PASS.

- [ ] **Step 5: Commit** `feat(console): FastMile telemetry probe path list`.

### Task 2: The Read parameters panel

**Files:**
- Create: `frontend/src/components/ReadParametersPanel.tsx`
- Test: `frontend/src/components/ReadParametersPanel.test.tsx`

**Interfaces:**
- Consumes: `probePaths`, `parsePaths`, `checkPaths`, `MAX_PROBE_PATHS` from Task 1; `api.getParametersLive(id: string, paths: string[]): Promise<{ command_key: string; status: string }>`; `ApiError` from `../api/client`; `toast(message: string, kind?: "error")` from `../lib/toast`.
- Produces: `export function ReadParametersPanel(props: { deviceId: string; names: Record<string, boolean> | null; writable: boolean }): JSX.Element` (a `div.panel` with an `h3` reading "Read parameters now").

Behaviour the tests pin: a labelled textarea "Paths to read, one per line"; a button "Use FastMile telemetry preset" (disabled when `names` is `null`/empty, with the reason shown in text) that fills the textarea from `probePaths(names, {includeCoordinates})`; a checkbox "Include the location coordinates (read once, to learn their format)" unticked by default; a button "Read now". "Read now" is disabled while a request is in flight, when `writable` is false, when there are no paths, or when `checkPaths` reports unknown paths, object paths or too many; each blocking reason is written out under the box, naming the paths. On success it calls `api.getParametersLive` once, shows `toast("Queued <command_key>. It runs when the device next checks in.")`, and unticks the coordinates box; on an `ApiError` it shows `toast(error.message, "error")`.

- [ ] **Step 1: Write the failing tests** in `ReadParametersPanel.test.tsx`, mocking `../api/client` (`getParametersLive`, `ApiError`, as `SimPanel.test.tsx` does) and `../lib/toast`; reuse the Task 1 `fastmile` fixture (export it from a shared test helper or copy the entries needed). Tests:
  - `fills the textarea with the preset and omits the coordinates` (assert a path such as `Device.DeviceInfo.UpTime` is in the textarea and no `_Latitude`).
  - `adds the three coordinate paths only when the box is ticked`.
  - `sends the trimmed, de-duplicated paths once, even on a double click` (`getParametersLive` called once with `("gw-1", [...])`, then the box is unticked).
  - `blocks a path the device does not have and names it` (type `Device.Nope.X`; the button is disabled; the text contains `Device.Nope.X`).
  - `blocks an object path ending in a dot` and `blocks more than 60 paths`.
  - `is disabled for a read-only role and says why` (`writable={false}`; button disabled; `getParametersLive` never called).
  - `says the model has not been discovered when names is null` (preset button disabled, reason shown; typing a path and reading still works).
  - `shows the API's message when the read cannot be queued` (reject with `new ApiError(409, "device is busy")`; `toast` called with that message and `"error"`).

- [ ] **Step 2: Run to verify failure.** Run: `npx vitest run src/components/ReadParametersPanel.test.tsx`. Expected: FAIL, component not found.

- [ ] **Step 3: Implement `ReadParametersPanel`** in `ReadParametersPanel.tsx` with local state for the text, the checkbox and a `busy` flag set before the request and cleared in `finally` (that is what makes a double click send once). Follow the panel markup, `className="btn"` buttons and `dim` helper text used in `SimPanel.tsx` and the "Object management" panel.

- [ ] **Step 4: Run to verify they pass**, same command. Expected: PASS.

- [ ] **Step 5: Commit** `feat(console): Read parameters now panel`.

### Task 3: Put the panel on the Parameters page

**Files:**
- Modify: `frontend/src/screens/DeviceDetail.tsx` (the `section === "parameters"` block, above the "Parameter cache" panel; add one import)
- Modify: `frontend/src/screens/DeviceDetail.workspace.test.tsx`

**Interfaces:**
- Consumes: `ReadParametersPanel` from Task 2; `id`, `paramNames` and `writable` already defined in `DeviceDetail`.

- [ ] **Step 1: Extend the failing test.** In the `shows each sub-page's own tools only` test, after `open("parameters")` and the existing `Object management` assertion, add `expect(screen.getByRole("heading", { name: /Read parameters now/ })).toBeTruthy();`. Run `npx vitest run src/screens/DeviceDetail.workspace.test.tsx`. Expected: FAIL on the new assertion.

- [ ] **Step 2: Render `<ReadParametersPanel deviceId={id} names={paramNames} writable={writable} />`** as the first child of the Parameters section.

- [ ] **Step 3: Run that test file, then the whole suite and the checks.** Run: `npx vitest run src/screens/DeviceDetail.workspace.test.tsx` (Expected: PASS), then from the repository root `bash scripts/ci-local.sh --only spec,frontend` (Expected: "All selected checks passed.").

- [ ] **Step 4: Commit** `feat(console): Read parameters now on the Parameters page`.

### Task 4: The procedure document and the pull request

**Files:**
- Create: `docs/CPE-PROBE.md`

- [ ] **Step 1: Write `docs/CPE-PROBE.md`** with these sections, and nothing in it beyond them:
  1. *Purpose*: one paragraph, pointing at the design spec.
  2. *Running the probe*: open the device, Parameters, "Read parameters now", "Use FastMile telemetry preset", tick the coordinates box once, Read now; the read runs at the device's next check-in (state the CPE's current interval, from the Availability panel's "Reports every"); then search the Parameter cache for the paths.
  3. *The link-drop test* (lab CPE only, it interrupts service for several minutes): note the time, read `Cellular.Interface.1.LastChange` and `UpTime`, pull the SIM (or antenna) for about five minutes, reconnect, wait for two check-ins, read again, and write down whether `LastChange` reset and by how much.
  4. *What to record*: a table with the columns path, value form, unit, notes; the rule that coordinates are recorded as a format only (units, precision, what no-fix looks like) and never as a position.
  5. *Findings*: the questions to answer, each with "to be filled in after the lab run": does `LastChange` reset on a link drop and in what unit; which of `IP.Interface.1` and `.2` carries the WAN address; the `Location.1.Source` values and whether a fix is present; the coordinate format; whether both the 5G and LTE signal leaves return values; any path that faulted.

- [ ] **Step 2: Run the checks and open the pull request.** Run: `bash scripts/ci-local.sh --only spec,frontend`. Expected: all pass. Push the branch and open a PR stating the stages, the checks that ran, and that the lab run (Task 5) follows deployment.

- [ ] **Step 3: Commit** `docs: CPE probe procedure`.

### Task 5: Run the probe and record the findings (after deployment; needs the operator)

This task is performed after Tasks 1 to 4 are merged and the lab is updated. It needs the operator to be at the lab CPE for the link-drop test.

- [ ] **Step 1: Run the probe** as written in `docs/CPE-PROBE.md` section 2; read the values in the Parameter cache. Expected: every preset path returns a value, or the faulted paths are listed.
- [ ] **Step 2: Run the link-drop test** (section 3) with the operator. Expected: a clear yes or no on whether `LastChange` resets, and its unit.
- [ ] **Step 3: Fill in the Findings section**, with the coordinate format only, and open a pull request.
- [ ] **Step 4: Stop.** The operator reads the findings and decides whether stage 2b (link changes and location) proceeds, and in what shape. Stage 2a (signal trend) is planned separately and does not wait for this.
