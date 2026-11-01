# Events: webhooks and replay

How an integration learns what happens to its devices and sales without
polling, and how it catches up after downtime. Recorded examples are in
[`contract/fixtures/acs/`](../../contract/fixtures/acs/).

## The envelope

Every event has the same shape, in a webhook body and in replay.
`occurred_at` is always UTC (`Z`), as is every timestamp on `/bss/v1`:

```json
{
  "id": "21c11827-9233-42ab-9c35-fd0896f74553",
  "type": "DeviceSeen",
  "occurred_at": "2026-10-31T05:59:40.15917Z",
  "sequence": 1,
  "customer_id": "985e6a30-89f0-43ff-b926-5c47f942db54",
  "account_id": "contract.0001",
  "device": { "id": "173ed01f-…", "oui_serial": "A4FC77+3TG02241+CONTRACT01" },
  "data": { "firstContact": true, "onlineStatus": "ONLINE", "lastInformAt": "…" }
}
```

Webhook bodies also keep the older TMF688 keys (`eventId`, `eventType`,
`eventTime`, `sourceKey`, `accountId`, `deviceId`, `serviceId`, `event`) so
subscribers that predate the envelope keep working.

`customer_id`, `account_id` and `device` can be **null**: `customer_id` for an
event of a device that belongs to no customer (a customer-bound client never
sees those), `account_id` for a device event of an unmapped device, `device`
for an onboarding event of a sale no CPE has contacted (an expiry, a BSS
retry or cancel). Timestamps the adapter writes end in `Z`; timestamps
inside `data` that come from the database read `+00:00`. Both are UTC: parse
RFC 3339.

`data` never carries secrets or full SIM identifiers. Do not trust
`customer_id` or `account_id` in a payload for authorization; look them up
in your own records.

## Event types

| Type | When | `data` |
|---|---|---|
| `DeviceSeen` | First contact ever, or back ONLINE from OFFLINE/UNREACHABLE; also when a CPE that already connected joins a customer (`joinedCustomer`: a pre-provisioned CPE's first contact belonged to no customer until its sale bound it) | `firstContact`, `joinedCustomer`, `onlineStatus`, `previousStatus`, `lastInformAt`, `ouiSerial` |
| `DeviceUnreachable` | No contact for the unreachable threshold (90 minutes by default) | `onlineStatus`, `previousStatus`, `lastInformAt`, `ouiSerial` |
| `FirmwareChanged` | The reported software version changed (learning it the first time is not a change) | `previousVersion`, `version`, `ouiSerial` |
| `OnboardingStateChangeEvent` | A sale moved state ([ONBOARDING.md](../ONBOARDING.md)) | `onboardingId`, `externalId`, `serialNumber`, `state`, `previousState`, `reason`, `reasonCode`, `source` |
| `IdentityMismatch` | The CPE or SIM is not what was sold | `onboardingId`, `externalId`, `serialNumber`, `reasonCode`, `reason` (masked identifiers) |
| `DeviceFault` | The CPE answered a job with a fault (an alarm is raised for the condition) | `protocol`, `jobId`, `faultCode`, `message` |
| `DeviceRecovered` | A later job of the same type on that CPE succeeded, clearing the alarm; once per alarm | `protocol`, `faultCode`, `conditionKey`, `recoveredAt` |
| `CellularStateChanged` | Registration, operator, cell, RAT, SIM status or APN changed | `protocol`, `changes` |

**`reasonCode`** values: `DEVICE_OTHER_CUSTOMER`, `SIM_MISMATCH`,
`PLMN_NOT_ALLOWED`, `SIM_UNREADABLE`, `CONFIG_FAILED` (holds and failures, which
wait for an operator), and `WAITING_SIM_READ`, `WAITING_OWN_CREDENTIAL`,
`WAITING_DISCOVERY`, `WAITING_CONFIG` (progress, not problems), and `TTL_EXPIRED`:
a sale no CPE contacted within the sale TTL (`ACS_BSS_SALE_TTL`, default 30 days) is
**CANCELLED**, freeing the serial for a new sale, and its TMF641 item FAILED. Read a
sale's current state with `GET /bss/v1/onboardings/{onboardingId}`.

Onboarding states are `PLANNED`, `CONTACTED`, `SECURED`, `DISCOVERED`,
`CONFIGURING`, `IN_SERVICE`, `ON_HOLD`, `FAILED`, `CANCELLED`.
`AWAITING_DEVICE` is not an onboarding state: it is the status of the mapping
request (202) and of the TMF641 `add` item while the sale waits.

**Which account an event carries.** Device events carry the device's current
BSS account (its active mapping), else none; onboarding events carry the
sale's account when the sale's customer owns it. Account-scoped webhooks
therefore receive a device's events only once it is mapped to the account;
replay by customer receives all of them.

## Sequence and ordering

Each event belongs to one ACS customer: the owner of its BSS account, else the
device's customer. Events with neither form a separate stream.

`sequence` **increases strictly within a customer** and numbers commit in order,
so a reader never sees a lower number appear after a higher one. It is not
gap-free: a skipped number is never a lost event. Detect missed events with
replay, not by counting.

## Webhooks

Subscribe with `POST /bss/v1/webhooks` (`account_id`, `target_url`, `secret`,
`event_types`). **You supply the secret** and keep custody of it; no response
ever returns it. Only `rotate-secret` generates one, returned once. Each
delivery is signed in the Standard Webhooks style: `Webhook-Id`,
`Webhook-Timestamp`, `Webhook-Signature: v1,<signature>` and
`X-Webhook-Event`. A `whsec_` secret gives standard base64 signatures.

**`Webhook-Id` is the delivery id**: one delivery per event per subscription,
stable across retries and redelivery. Deduplicate deliveries on it, and events
on the envelope `id` (the same event reaches two subscriptions as two
deliveries). The delivery view gives each delivery's `event_id` and
`event_sequence`.

- **Retries:** exponential backoff, 8 attempts, then the delivery is `FAILED`.
- **Delivery view:** `GET /bss/v1/webhooks/{id}/deliveries?status=` shows status,
  attempts and the last error of each delivery.
- **Redeliver:** `POST /bss/v1/webhooks/{id}/deliveries/{delivery_id}/redeliver`
  queues a delivery again from its first attempt, with the same `Webhook-Id`:
  `202 {"id": "<delivery_id>", "status": "PENDING"}`.
- **Secret rotation:** `POST /bss/v1/webhooks/{id}/rotate-secret`
  (`overlap_seconds`, default 86400, at most 7 days) returns the new secret
  once. Until `previous_secret_valid_until`, `Webhook-Signature` carries one
  signature per secret (`v1,<new> v1,<previous>`). Accept a delivery if **any**
  signature verifies, then switch to the new secret.
- Targets must pass the network policy (no loopback, link-local or metadata
  addresses).

## Replay

`GET /bss/v1/events?after=<sequence>&limit=` (scope `tmf:read`) returns the
caller's customer's events with `sequence > after`, oldest first:

```json
{ "events": [ … ], "next_after": 2, "latest_sequence": 2, "purged_through": 0, "gap": false }
```

- Pass `next_after` back as `after` until `events` is empty. Store the last
  cursor you processed. On an empty page `next_after` equals the `after` you
  sent.
- `limit` is 1 to 500; a value outside that range (or none) means 100.
- A **customer-bound client** reads its own customer. A global client names one
  with `customer_id` (empty = events with no customer). Account-listed clients
  are refused; they use the TMF688 lists.
- **Retention:** events are kept indefinitely today. When retention removes
  events, `purged_through` is the highest sequence number removed, and
  `gap: true` means `after < purged_through`. That is conservative: it is also
  true when the removed range happened to contain only skipped numbers.
  Reconcile from current state, then continue from `latest_sequence`.

## Errors on these endpoints

Errors are RFC 9457 problems (`type`, `title`, `status`, `detail`, `code`) that
keep the older `error` and `message` keys; see
[02-integration-model.md §3](02-integration-model.md#3-error-classes--what-to-retry-and-what-not-to).

| Status | `error` | When |
|---|---|---|
| 400 | `ErrInvalidRequest` | Bad parameter, e.g. a negative `after` or an unknown delivery `status` |
| 401 | `invalid_token`, `ErrUnauthorized` | No or bad bearer token (switch on the status; see 02 §3) |
| 403 | `insufficient_scope` | The token lacks the scope (`WWW-Authenticate: Bearer error="insufficient_scope", scope="tmf:read"`) |
| 403 | `ErrClientNotCustomerBound`, `ErrReplayNeedsCustomer` | A global client where only bound clients are accepted; an account-listed client asking for replay |
| 404 | `ErrNotFound` | Unknown, or another customer's, subscription or delivery |
| 429 | `ErrRateLimited` | Over the per-client rate (default 5 requests/s, burst 10; `ACS_BSS_RATE_LIMIT_PER_SECOND`, `ACS_BSS_RATE_LIMIT_BURST`), with `Retry-After` in seconds |

A robust consumer takes webhooks for speed and replays on start-up and on a
timer, deduplicating on the event `id`.
