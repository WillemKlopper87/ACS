# Integration contract fixtures

Requests and responses **recorded from a running ACS** (cmd/api,
cmd/bssadapter and Postgres), for integrators' conformance tests. Each file
is in the fixture format the bss-oss-integration harness loads: `name`,
`description`, `captured_from` (commit and date), and `exchanges` with
`method`, `path`, `status`, `mutating`, the request body where there is one,
and the response `body`.

| Fixture | Shows |
|---|---|
| `oauth-token-customer-bound` | Token response of a customer-bound client (`customer_id`) |
| `mapping-create` | First write to a new account; the account and device join the caller's customer |
| `mapping-pre-provision` | `pre_provision` for an unseen CPE: 202, `AWAITING_DEVICE`, `href` and `Location` to the sale read, `plan_matched` |
| `mapping-idempotency-key` | `Idempotency-Key` on a mapping: first answer, replay (`Idempotent-Replayed`), same key with another body (409) |
| `mapping-unknown-plan` | A plan code the ACS does not know: `plan_matched: false` and a `PLAN_NOT_FOUND` warning |
| `sale-read` | `GET /bss/v1/onboardings/{id}`, then an unknown sale as a problem (`application/problem+json`) |
| `sale-retry-cancel` | A held sale retried by the BSS, cancelled, and cancelled again (409 `ErrSaleWrongState`) |
| `devices-page` | `GET /bss/v1/devices` in pages with `next_cursor` |
| `tmf641-delete-order` | TMF641 delete without `unassignReason`: completed |
| `tmf641-repeat-external-id` | The same `externalId` again: the same order replays (200), another order is 409 |
| `tmf641-delete-unmapped` | A delete of a role with no device: completed |
| `tmf641-order-failed` | A delete naming another device than the one in the role: failed, nothing ended |
| `tmf641-order-partial` | A completed delete, then a failed modify: partial |
| `sale-failed-retry` | A FAILED sale (CONFIG_FAILED) read, then retried by the BSS |
| `sale-ttl-expired` | A sale expired by the TTL: CANCELLED/TTL_EXPIRED, its TMF641 add FAILED, the event |
| `idempotency-key-in-flight` | A retry while the first request runs: 409 `ErrIdempotencyKeyInFlight` with `Retry-After` |
| `secret-handle-order` | A Wi-Fi passphrase as a sealed handle: stored, preflighted, claimed by an order, refused on reuse, opened when the CPE is written |
| `secret-handle-tmf` | A handle in a TMF641 modify item (reuse FAILS the item) and a TMF640 `WiFiPasswordHandle` PATCH |
| `secret-handle-errors` | `POST /bss/v1/secrets` refused: TTL out of range, account-listed client, plain HTTP, no key |
| `secret-handle-expired` | A handle that expired while its job waited: the job FAILS with `SECRET_HANDLE_UNAVAILABLE` |
| `errors` | Problem bodies for 400, 403 `insufficient_scope` (with `WWW-Authenticate`), 401 and 429 (with `Retry-After`) |
| `tmf641-add-awaiting-device` | TMF641 add for an unseen CPE; the modify behind it stays `PENDING` |
| `events-replay` | Replay page in the event envelope, then the empty page after it |
| `webhook-create`, `webhook-deliveries`, `webhook-rotate-secret` | Subscription, delivery view with the last error, secret rotation |
| `webhook-bodies` | One delivered webhook body per event type (DeviceSeen, DeviceUnreachable, FirmwareChanged, CellularStateChanged, DeviceFault, DeviceRecovered, OnboardingStateChangeEvent, IdentityMismatch), as sent, from real CWMP sessions with cmd/acs |
| `tmf640-patch-monitor` | TMF640 PATCH with `X-Idempotency-Key`, its replay, the Monitor, an unknown Monitor (404) |
| `not-found-other-account` | Another customer's account reads as 404 |
| `subscriber-profile` | **1.2.** `PUT /bss/v1/accounts/{id}/subscriber`: set name and VIP, change one field, clear the name, a name over 120 characters (400), an account ID another customer also uses (200 since 2.0: it is this customer's own account) |
| `sale-ended` | **1.2.** A sale an operator ended: the BSS reads it as `ENDED`, the account lists no device, a cancel is 409, and the `OnboardingStateChangeEvent` with `ENDED` |
| `sale-swap` | **1.2.** A device swap: the old sale `ENDED`, a new `PLANNED` sale for the replacement, and an event for each |
| `sale-plan-change` | **1.2.** A plan change that writes configuration: the sale reads `DISCOVERED` with the new plan code, and its event |
| `ids-per-customer` | **2.0.** A second customer uses an `account_id` and an `external_order_id` another customer already uses: both are its own (200, 202), and each customer reads only its own device |
| `order-resend-changed` | **2.0.** An `external_order_id` sent again: the identical request replays the order (202), a different request is 409 `ErrIdempotencyKeyReused` |
| `webhook-secret-too-short` | **2.0.** A new webhook subscription with a secret under 32 characters: 400 |

All fixtures were recorded again for 2.0.0, at one commit (see
`captured_from`). Apart from ids, times and the three new fixtures, one
exchange differs from the 1.2.1 recording: the last one of
`subscriber-profile`, described below. The operator calls in some fixtures
(`/api/v1/onboardings/...`) are not part of the BSS contract; they are shown
so you can see what produces the state and events you read.

Secrets, tokens and SIM identifiers are redacted. Ids, timestamps and sequence
numbers are real values from the recording; compare shapes and states, not
these values. Exchanges keep the response headers an integration acts on
(`headers`: `Location`, `Idempotent-Replayed`, `Content-Type`, `Retry-After`)
and the request headers that matter (`request_headers`). Errors are RFC 9457
problems that keep the older `error`/`message` keys.

## 2.1.0: device operations and a status read

2.1.0 only adds. A 2.0.0 integration needs no change.

| Addition | What it is |
|---|---|
| `REBOOT` | An order action (`POST /bss/v1/orders`) that restarts the account's CPE. No `parameters`. Tracked like any order (`GET /bss/v1/jobs/{command_key}`); the CPE is back when it next reports in |
| `REFRESH_STATUS` | An order action that asks the CPE to report its status parameters now. No `parameters`; it changes nothing on the CPE. Its job fails when the device has no status parameter to report |
| `FACTORY_RESET` | An order action that wipes the CPE's configuration. Refused with `400 ErrUnsupportedAction` unless the operator set `ACS_BSS_ALLOW_FACTORY_RESET=true` (off by default), and the order must carry `parameters: {"confirm": "FACTORY_RESET"}` |
| `GET /bss/v1/mappings/{account_id}/status` | Uptime, LAN host count and, on a cellular router, signal (RSRP, RSRQ, SINR, RSSI), from the ACS's parameter cache. A value the CPE has not reported is `null`. `read_at` is when the oldest returned value was reported |

The three actions are also accepted by `POST /bss/v1/actions/preflight`,
which answers whether the order would be taken and writes nothing. A resend
of the same `external_order_id` with the same action is a retry and does not
restart the CPE twice; with a different action it is `409
ErrIdempotencyKeyReused`, as for every order since 2.0.0.

**No fixture is recorded for 2.1.0 yet.** The fixtures in this directory are
the 2.0.0 recording and remain valid. The additions are covered by the
adapter's tests (`backend/cmd/bssadapter/operations_test.go`); recordings of
a `REBOOT` order, a refused `FACTORY_RESET` and the status read are to be
added with `scripts/contract/record_fixtures.py` against a throwaway stack
before `bss-contract-v2.1.0` is tagged.

## 2.0.0: what changed for an integration

2.0.0 is a major version because three answers changed meaning. Requests
and response shapes are unchanged; a customer-bound client that never met
one of these cases needs no change.

| Situation | Up to 1.2.1 | 2.0.0 |
|---|---|---|
| A write to an `account_id` that another customer also uses | `404` | Succeeds: it is the caller's own account (`ids-per-customer`; the last exchange of `subscriber-profile`, recorded as 404 in 1.2.1, is now 200) |
| An `external_order_id` (or TMF641 `externalId`) that another customer used | `409 ErrIdempotencyKeyReused` | Succeeds as the caller's own order (`ids-per-customer`) |
| An `external_order_id` or TMF640 `X-Idempotency-Key` sent again with a different request | `202` with the original order | `409 ErrIdempotencyKeyReused` (`order-resend-changed`). An identical resend is unchanged |
| A new webhook subscription with a `secret` under 32 characters | Accepted | `400 ErrInvalidRequest` (`webhook-secret-too-short`) |
| A new webhook subscription with a plain `http` `target_url`, production profile | Accepted | `400 ErrInvalidRequest` |

`account_id`, `external_order_id`, the TMF641 `externalId` and the
event/alarm `sourceKey` are unique per customer, not across the ACS
(`docs/TENANT-SCOPED-IDS-DESIGN.md`). Existing webhook subscriptions are
not affected by the two webhook rules.

New codes, none of which a customer-bound client on an allowed network
can receive in normal operation: `409 ErrAccountAmbiguous` (a fleet-wide
client names an ID that more than one customer uses), `403
ErrSourceNotAllowed` (a call from outside the networks the operator
allows) and `503 ErrIntegrationDisabled` (the ACS runs without the
integration).

**Versioned.** The contract is tagged `bss-contract-v<major>.<minor>.<patch>`
(`bss-contract-v1.0.0`, `v1.0.1`, `v1.1.0`, `v1.1.1`, `v1.2.0`, `v1.2.1`, `v2.0.0`). A tag never moves. Within a major version, changes only add
(fields, endpoints, and codes that only a new endpoint or parameter can return); a removal or a changed
meaning bumps the major version. 1.1.0 added sealed secret handles (`POST /bss/v1/secrets`). 1.2.0 added the optional `subscriber_name` and `vip`, `PUT /bss/v1/accounts/{id}/subscriber` and the `ENDED` sale state (a consumer that validates the state against a fixed list must accept it). 1.2.1 made a plan change update the account mapping's `service_plan` too (it only changed the sale's), corrected the spec text on ending a sale in service (a TMF641 delete ends the mapping and leaves the sale `IN_SERVICE`), and named `ENDED` in the cancel 409 message. 2.0.0 made IDs unique per customer, refused a resent order that differs, and set the webhook secret rules (table above). 2.1.0 (not tagged yet) added the device operations `REBOOT`, `REFRESH_STATUS` and `FACTORY_RESET` and the status read. Pin integration tests to a tag. Re-record
with [`scripts/contract/record_fixtures.py`](../scripts/contract/record_fixtures.py)
against a throwaway stack (its docstring lists the environment it needs).
