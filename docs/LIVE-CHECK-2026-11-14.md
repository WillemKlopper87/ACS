# Live check: 2026-11-14

The stack was run locally at the head of this branch and walked as an operator and as a BSS/NaaS integrator would. This records what was run, what was found, what was fixed on the spot, and what is left.

## 1. What was run

- PostgreSQL 16 (the repository targets 18; all 95 migrations applied cleanly on 16), `cmd/acs`, `cmd/api`, `cmd/bssadapter`, `cmd/uspc`, and the built console.
- Fifteen simulated CPEs (Zyxel NR5103 ×6, Nokia 3TG02241 ×4, Huawei N5368X ×3, Teltonika RUTX50 ×2) from a small CWMP simulator written for this check: Digest auth, Inform, and answers to GetParameterNames, GetParameterValues, SetParameterValues and Reboot. It is not committed.
- The gateway ran with `ACS_CWMP_ALLOW_SHARED_ESTABLISHED=true`, the lab setting that lets one shared credential serve established devices, because the simulator does not implement credential graduation. The production path (bootstrap, then a per-device credential) was therefore **not** exercised.
- A headless browser visited all 20 console screens and the nine device sections, recording console errors and failed API calls.
- The NaaS quickstart (`docs/integration/01-quickstart.md`) was followed against the adapter with two customer-bound OAuth clients, one per customer.

Not covered: webhook delivery (the adapter refuses loopback targets, correctly, so a local receiver cannot be used), USP devices, firmware, and anything needing a real radio.

## 2. Step 1 of the device plan, live

A bulk parameter write to all 15 devices behaved as designed:

| Moment | Queued | Succeeded | Failed | Cancelled |
|---|---|---|---|---|
| Six devices checked in; three more refused the write | 6 | 6 | 3 | 0 |
| After "retry failed" and those three checking in again | 6 | 9 | 0 | 0 |
| After "cancel queued" | 0 | 9 | 0 | 6 |

A cancelled device that checked in afterwards was not written. One change came out of watching it: "retry failed" first also re-queued cancelled jobs, which undoes a deliberate cancel. It now retries failed and timed-out jobs only.

## 3. Console findings

The walk produced no page errors. Two API errors appeared, both cosmetic (C7, C8).

| # | Finding | Status |
|---|---|---|
| C1 | **Jobs**: the new Cancel/Retry buttons were pushed off the right edge by a long fault text. | Fixed: the buttons sit next to the status. |
| C2 | **Jobs**: the device column shows the first eight characters of a UUID, which no operator recognises. | Partly fixed: it now links to the device. Showing the serial needs the jobs API to return it. |
| C3 | **Inventory and Bulk actions**: no firmware version, customer or label column, and the filter box says it covers only the loaded page. | Open (plan step 2). |
| C4 | **Bulk actions**: the action panel only appears after rows are ticked, so the screen opens as a second inventory list with no hint of what it can do. | Open. Show the actions up front, disabled until a selection exists. |
| C5 | **Device overview**: identity and recent jobs only. Firmware version, uptime, signal, connected Wi-Fi clients and the owning account are one click away each; the first screen has most of its width empty. | Open. A health summary row belongs here. |
| C6 | **BSS / NaaS → Onboarding/Setup**: the mapping form says the device "must already have sent at least one Inform", but pre-provisioning for unseen devices exists. The webhook form is described as "JOB_COMPLETED delivery targets" although eight event types are deliverable. | Open (text only). |
| C7 | **Login**: one `401 GET /devices` fires before sign-in completes. | Open (harmless, noisy in logs). |
| C8 | **Device → Connectivity**: `404 GET /devices/{id}/vpn/config` for every device with no VPN peer, logged as an error in the browser console. | Open. Return 200 with an empty state, or do not ask until enrolled. |
| C9 | **Sidebar**: at 900 px height the last three groups (Integrations, Administration, Records) are below the fold behind the theme picker, with no scroll cue. | Open. |

## 4. NaaS integration findings

What held up: tokens, account claiming, tenant isolation on mappings (another customer's device answers 404, indistinguishable from an unknown one), order dispatch, idempotent resend, read-back confirmation before `SUCCESS`, and problem-shaped errors. Calls answered in 2 to 20 ms locally. An order placed through the adapter wrote both primary SSIDs on the device's next session and reported `SUCCESS`.

| # | Finding | Status |
|---|---|---|
| N1 | **Cross-tenant order ID collision.** `external_order_id` is unique across all accounts. A second customer's client that reused an ID got `202` with the **first customer's `command_key` and job status**, and its own order was silently never placed. The TMF640 PATCH path already refuses this; the legacy `POST /bss/v1/orders` did not. | **Fixed**: `409 ErrIdempotencyKeyReused`, with a regression test. See §5. |
| N2 | **Same ID, different body, same account** is answered with the original order and `202`. A caller that changes an order and resends it under the same ID is told it was accepted. TMF641 already answers 409 for this. | Open: changing it alters the meaning of an existing endpoint, which the contract rules reserve for a major version. Decision needed. |
| N3 | **Any customer-bound client can claim an unassigned device by serial.** Mapping a device with no customer moved it to the caller's customer. This is the claimable-pool design, but a serial is printed on the box. | Open: decide whether claiming needs a prior sale record (strict mode) in production. |
| N4 | **Quickstart disagreed with the API in three places**: an unknown account is `404` for a customer-bound client, not an empty list; a webhook needs a caller-chosen `secret` and is not issued one; a customer-bound client cannot create a fleet-wide subscription. | **Fixed** in the quickstart. |
| N5 | A customer-bound client asking for a fleet-wide webhook gets `404 resource not found`, which reads as a wrong URL. | Open: answer `403` with the reason. |
| N6 | No `X-Request-Id` on responses and no rate-limit headers (`RateLimit-Remaining` or similar) on success, so an integrator cannot quote a request to support or pace itself before a `429`. | Open (additive, contract-safe). |
| N7 | The `401` seen carried no `WWW-Authenticate` header and was served as `application/json`, although its body is an RFC 9457 problem and the contract describes errors as such. | Open (additive). |
| N8 | The service plan in a mapping is free text: an unknown plan maps the device and returns a `PLAN_NOT_FOUND` warning inside a `200`. An integrator that does not read warnings believes the plan was applied. | Open: the warning is correct; consider a strict flag that turns it into a refusal. |

## 5. Contract note for N1

N1 adds a `409` to `POST /bss/v1/orders`. It uses a code the contract already has (`ErrIdempotencyKeyReused`), and the only callers that can see it are ones that were previously being handed another account's order. It is documented in `backend/openapi-bssadapter.yaml`, but the contract version and tag were **not** bumped and no fixture was recorded: that is a release decision, and the fixture recorder needs its own throwaway stack.
