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

Secrets, tokens and SIM identifiers are redacted. Ids, timestamps and sequence
numbers are real values from the recording; compare shapes and states, not
these values. Exchanges keep the response headers an integration acts on
(`headers`: `Location`, `Idempotent-Replayed`, `Content-Type`, `Retry-After`)
and the request headers that matter (`request_headers`). Errors are RFC 9457
problems that keep the older `error`/`message` keys.

**Versioned.** The contract is tagged `bss-contract-v<major>.<minor>.<patch>`
(`bss-contract-v1.0.0`, `v1.0.1`, `v1.1.0`). A tag never moves. Within a major version, changes only add
(fields, endpoints, and codes that only a new endpoint or parameter can return); a removal or a changed
meaning bumps the major version. 1.1.0 added sealed secret handles (`POST /bss/v1/secrets`). Pin integration tests to a tag. Re-record
with [`scripts/contract/record_fixtures.py`](../scripts/contract/record_fixtures.py)
against a throwaway stack (its docstring lists the environment it needs).
