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
| `mapping-pre-provision` | `pre_provision` for an unseen CPE: 202, `AWAITING_DEVICE` |
| `tmf641-add-awaiting-device` | TMF641 add for an unseen CPE; the modify behind it stays `PENDING` |
| `events-replay` | Replay page in the event envelope, then the empty page after it |
| `webhook-create`, `webhook-deliveries`, `webhook-rotate-secret` | Subscription, delivery view with the last error, secret rotation |
| `webhook-bodies` | One delivered webhook body per event type (DeviceSeen, DeviceUnreachable, FirmwareChanged, CellularStateChanged, OnboardingStateChangeEvent ON_HOLD and CONTACTED, IdentityMismatch), as sent, from real CWMP Informs to cmd/acs |
| `tmf640-patch-monitor` | TMF640 PATCH with `X-Idempotency-Key`, its replay, the Monitor, an unknown Monitor (404) |
| `not-found-other-account` | Another customer's account reads as 404 |

Secrets, tokens and SIM identifiers are redacted. Ids, timestamps and sequence
numbers are real values from the recording; compare shapes and states, not
these values.

**Not yet a versioned contract.** These fixtures follow the branch they were
recorded on and change with it until the contract is tagged. Re-record with
[`scripts/contract/record_fixtures.py`](../scripts/contract/record_fixtures.py)
against a throwaway stack (its docstring lists the environment it needs).
