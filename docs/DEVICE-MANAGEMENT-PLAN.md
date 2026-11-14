# Device management: findings and plan

How an operator manages one CPE, and how they manage many, as the code stood on 2026-11-14 (`a61d185`), what limits the workflow, and the order to fix it in. Step 1 of the plan is implemented with this document; see §4.

Related: `docs/CONSOLE-UX-PROPOSAL-2026-10-30.md` (device workspace, navigation, estates and Wi-Fi campaigns) and `docs/ASSESSMENT-2026-11-09.md` (platform-level findings).

## 1. Summary

The device page is rich: nine sections covering Wi-Fi, cellular and SIM, connectivity, diagnostics, firmware, ownership, parameters and remote access. The weak spots are elsewhere:

- **After the button.** Once an action is queued the operator has little control: until this change a job could not be cancelled or retried, and a bulk action left no record beyond its HTTP response.
- **Devices as a set.** Groups are static lists, the fleet list is filtered in the browser over one page, and "select all matching" knows four filters.

## 2. Findings

References are `file:line` at `a61d185`.

### 2.1 One device

| # | Finding | Evidence |
|---|---|---|
| I1 | A job could not be cancelled or retried. A wrong write queued for an offline device ran whenever it next informed. **Fixed in step 1.** | Only `GET /jobs` and `GET /jobs/{command_key}` existed (`backend/cmd/api/routes.go`) |
| I2 | Config backup is one-way: a file can be pulled from a device, but there is no restore, no parameter snapshot and no diff between two points in time. | `POST /devices/{id}/uploads`; no restore route |
| I3 | No single timeline. Jobs, parameter history, audit entries, onboarding events and alert incidents are five separate lists. | Device sections in `frontend/src/lib/deviceSections.ts` |
| I4 | No notes or support context on a device; only label, location and tags. | `deviceResponse`, `backend/cmd/api/device_handlers.go:26` |
| I5 | Policies correct drift but do not show it. Enforcement checks only parameters present in the Inform, writes every value as `xsd:string`, and there is no "compliant / drifted" view per device or per policy. | `backend/cmd/acs/enforce.go:32-56` |
| I6 | The Jobs screen is the newest 200 jobs, unpaged and unfiltered on the server. | `listLimit`, `backend/internal/jobs/job.go:367` |

### 2.2 Many devices

| # | Finding | Evidence |
|---|---|---|
| G1 | A bulk action was fire-and-forget: its per-device results existed only in the response. **Fixed in step 1.** | `bulkAction`, `backend/cmd/api/device_handlers.go:657` |
| G2 | Four bulk actions only (set parameter, set Wi-Fi, connection request, refresh cellular), capped at 500 devices, fanned out inside one HTTP request. No bulk reboot, firmware, tag or group assignment. | `backend/cmd/api/device_handlers.go:624-705` |
| G3 | `GET /devices` takes only page and page size. The Fleet, Groups and device-picker screens load one page (500 devices in Groups) and filter in the browser. | `listDevices`, `backend/cmd/api/device_handlers.go:177`; `frontend/src/screens/DeviceGroups.tsx:56` |
| G4 | "Select all matching" filters on manufacturer, online status, reachability and search only: not model, firmware version, tag, group, customer, plan or onboarding state. | `listMatchingDeviceIDs`, `backend/cmd/api/device_handlers.go:236` |
| G5 | Groups are static member lists: no rename, no rule-based membership, no group health view. | `backend/internal/store/migrations/0013_device_groups.sql`; no `PUT /device-groups/{id}` |
| G6 | Templates, policies, schedules, groups and alert policies can be created and deleted but not edited. | No `PUT`/`PATCH` on those routes |
| G7 | Schedules are interval-only (minimum 60 s): no run-once and no maintenance window. Only firmware rollouts have a window. | `backend/internal/scheduler/scheduler.go:61` |
| G8 | Policies and templates target by model and customer, not by group or tag. Rollouts target by model and current version. | `createPolicyRequest`, `createTemplateRequest`, `createRolloutRequest` |

## 3. Plan

In the order to build. Steps 1 to 3 are the foundation the rest stand on.

| Step | What | Closes |
|---|---|---|
| **1** | **Bulk-operation record; job cancel and retry.** Done, §4. | I1, G1 |
| 2 | Server-side device search: filters for model, firmware, tag, group, customer, plan, onboarding state and last-inform age; keyset paging; used by Fleet, Bulk actions, the device picker and Groups. Saved filters per operator. Page and filter the Jobs list the same way. | G3, G4, I6 |
| 3 | Dynamic groups: a group defined by a saved filter and evaluated when used, beside today's static groups. Let policies, templates, schedules and rollouts target a group. Group rename. | G5, G8 |
| 4 | More bulk actions through the same record: reboot, firmware, apply template by filter, add or remove a tag, add to a group. A preview before running ("312 devices, 40 offline") and a typed confirmation for destructive ones. Move the fan-out out of the HTTP request into a worker so the 500-device cap can go. | G2 |
| 5 | Maintenance windows and run-once scheduling for any bulk operation. | G7 |
| 6 | Edit in place for templates, policies, schedules, groups and alert policies, with the change audited. | G6 |
| 7 | Device timeline: one chronological feed of jobs, parameter changes, informs with their event codes, alerts and operator actions. | I3 |
| 8 | Config snapshot, diff and restore per device; "compare with template" before applying. | I2 |
| 9 | Compliance view: per policy, how many devices comply; per device, which policies it fails. Check the cached parameters, not only the Inform, and write the discovered type. | I5 |
| 10 | Group page as a dashboard: online and offline counts, firmware spread, open alerts, recent bulk operations. Device notes shown in the device header. | G5, I4 |

Steps 3, 4 and 10 are what the estates and Wi-Fi campaigns in the console proposal (§8) need underneath: a campaign is a scheduled bulk operation against a dynamic group, with a second one to revert it.

## 4. Step 1 as built

### 4.1 Job cancel and retry

| Route | Does |
|---|---|
| `POST /api/v1/jobs/{command_key}/cancel` | Cancels a job that is still `QUEUED`. It becomes `CANCELLED`. |
| `POST /api/v1/jobs/{command_key}/retry` | Queues a new job with the payload of one that ended `FAILED`, `TIMEOUT` or `CANCELLED`. The original is left as it is. |

Rules, enforced in `backend/internal/jobs/operator_actions.go`:

- **Only a queued job can be cancelled.** Once an RPC has gone out the device may have acted on it. The status test and the update are one SQL statement, so a job the gateway leases at the same moment is either cancelled first or the cancel is refused.
- **Workflow-owned jobs are refused** for both actions (409, with the reason): jobs created by the system, the BSS adapter or onboarding, and jobs referenced by a firmware rollout, a BSS order or a credential rotation. Those workflows track their jobs by outcome. This also keeps `CANCELLED` out of the BSS contract: a job a NaaS order created can never reach that state.
- **Retry covers repeatable types only**: set and get parameters, connection request, reboot, schedule inform, delete object, parameter attributes, ping and traceroute. Not firmware or file transfers (their URLs and tokens expire), `ADD_OBJECT` (a retry after an unknown outcome can create a second object), `FACTORY_RESET` or `PARAMETER_DISCOVERY`.
- **A job that carried a secret cannot be retried.** A finished job's secrets are erased from its payload (migration 0086, extended to `CANCELLED`), so there is nothing left to send. The API says so; the operator issues the action again.
- **Permission** is the one that queuing a job of that type needs (`devices.write`, `diagnostics.run`, `connection_request`, …), checked against the job's type, plus the device's tenancy scope.
- Both actions are audited (`JobCancelled`, `JobRetried`).

### 4.2 Bulk operations

Every bulk action and template apply now writes one `bulk_operations` row and one `bulk_operation_items` row per requested device (migration 0095, package `backend/internal/bulkops`). The response to the fan-out carries `bulk_operation_id`.

- The operation does not own the work. Each device still gets its own job; an item points at the latest job for its device, and progress is those jobs' statuses read when asked. Nothing has to be kept in step.
- A device that could not be queued is an item with the reason and no job.
- The label holds parameter names, never values.

| Route | Does |
|---|---|
| `GET /api/v1/bulk-operations` | Operations, newest first, with counts: queued, running, succeeded, failed, cancelled, not queued. |
| `GET /api/v1/bulk-operations/{id}` | One operation with each device's latest job. |
| `POST /api/v1/bulk-operations/{id}/cancel` | Cancels the jobs still queued. |
| `POST /api/v1/bulk-operations/{id}/retry-failed` | Queues a new job for each device whose job failed or timed out, and points the item at it. Cancelled jobs are left alone: they were withdrawn on purpose. Reports how many were retried and, by reason, how many were not. |

**Tenancy.** A scoped operator sees an operation only through the devices in their scope: counts and items cover those devices alone, cancel and retry touch only those, and an operation with none of their devices is a 404.

**Permissions.** Reading needs a read-only login. Cancel and retry need `bulk_actions`, or `template.manage` for a template apply.

### 4.3 Console

- **Jobs**: a Cancel button on queued jobs and a Retry button on failed, timed-out and cancelled jobs of a repeatable type. A refusal shows the server's reason.
- **Bulk operations** (new, under Devices): the list with state and progress; selecting one shows its counts, each device's outcome with failures first, and the Cancel queued and Retry failed buttons. It refreshes every five seconds.
- **Bulk actions**: the result banner links to the operation.

### 4.4 Not in step 1

- Scheduled jobs that target a group do not yet record an operation per run.
- Retrying an item that was never queued. The reason it was not queued still holds, so the action is run again for those devices.
- The fan-out still runs inside the request, so the 500-device cap stays until step 4.

## 5. Tests

- `backend/cmd/api/bulk_operation_integration_test.go`: operation lifecycle (progress, scoped views, scoped cancel, retry, repeat retry is a no-op), and job cancel/retry (queued only, workflow-owned refused, secret erased, non-repeatable types, scope and permission).
- `frontend/src/lib/bulkOperations.test.ts`: outcome naming and ordering, progress text, which jobs offer which action.
