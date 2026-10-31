# Events: webhooks and replay

How an integration learns what happens to its devices and sales without
polling, and how it catches up after downtime. Recorded examples are in
[`contract/fixtures/acs/`](../../contract/fixtures/acs/).

## The envelope

Every event has the same shape, in a webhook body and in replay:

```json
{
  "id": "21c11827-9233-42ab-9c35-fd0896f74553",
  "type": "DeviceSeen",
  "occurred_at": "2026-10-31T07:59:40.15917+02:00",
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

`data` never carries secrets or full SIM identifiers. Do not trust
`customer_id` or `account_id` in a payload for authorization; look them up
in your own records.

## Event types

| Type | When | `data` |
|---|---|---|
| `DeviceSeen` | First contact ever, or back ONLINE from OFFLINE/UNREACHABLE | `firstContact`, `onlineStatus`, `previousStatus`, `lastInformAt`, `ouiSerial` |
| `DeviceUnreachable` | No contact for the unreachable threshold (90 minutes by default) | `onlineStatus`, `previousStatus`, `lastInformAt`, `ouiSerial` |
| `FirmwareChanged` | The reported software version changed (learning it the first time is not a change) | `previousVersion`, `version`, `ouiSerial` |
| `OnboardingStateChangeEvent` | A sale moved state ([ONBOARDING.md](../ONBOARDING.md)) | `onboardingId`, `externalId`, `serialNumber`, `state`, `previousState`, `reason`, `reasonCode`, `source` |
| `IdentityMismatch` | The CPE or SIM is not what was sold | `onboardingId`, `externalId`, `serialNumber`, `reasonCode`, `reason` (masked identifiers) |
| `DeviceFault`, `DeviceRecovered` | A device fault was raised or cleared | `protocol`, `faultCode`, `message` / `recoveredAt` |
| `CellularStateChanged` | Registration, operator, cell, RAT, SIM status or APN changed | `protocol`, `changes` |

**`reasonCode`** values: `DEVICE_OTHER_CUSTOMER`, `SIM_MISMATCH`,
`PLMN_NOT_ALLOWED`, `SIM_UNREADABLE`, `CONFIG_FAILED` (holds and failures, which
wait for an operator), and `WAITING_SIM_READ`, `WAITING_OWN_CREDENTIAL`,
`WAITING_DISCOVERY`, `WAITING_CONFIG` (progress, not problems). `TTL_EXPIRED`
will come with the sale expiry in I3.

## Sequence and ordering

Each event belongs to one ACS customer: the owner of its BSS account, else the
device's customer. Events with neither form a separate stream.

`sequence` **increases strictly within a customer** and numbers commit in order,
so a reader never sees a lower number appear after a higher one. It is not
gap-free: a skipped number is never a lost event. Detect missed events with
replay, not by counting.

## Webhooks

Subscribe with `POST /bss/v1/webhooks` (`account_id`, `target_url`, `secret`,
`event_types`). Each delivery is signed in the Standard Webhooks style:
`Webhook-Id` (stable across retries; deduplicate on it), `Webhook-Timestamp`, and
`Webhook-Signature: v1,<signature>`. A `whsec_` secret gives standard base64
signatures.

- **Retries:** exponential backoff, 8 attempts, then the delivery is `FAILED`.
- **Delivery view:** `GET /bss/v1/webhooks/{id}/deliveries?status=` shows status,
  attempts and the last error of each delivery.
- **Redeliver:** `POST /bss/v1/webhooks/{id}/deliveries/{delivery_id}/redeliver`
  queues a delivery again from its first attempt, with the same `Webhook-Id`.
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
  cursor you processed.
- A **customer-bound client** reads its own customer. A global client names one
  with `customer_id` (empty = events with no customer). Account-listed clients
  are refused; they use the TMF688 lists.
- **Retention:** events are kept indefinitely today. When retention removes
  events, `purged_through` rises, and `gap: true` means events after your cursor
  were removed before you read them. Reconcile from current state, then
  continue from `latest_sequence`.

A robust consumer takes webhooks for speed and replays on start-up and on a
timer, deduplicating on the event `id`.
