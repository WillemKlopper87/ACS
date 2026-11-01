# Onboarding: from sale to service

Status: slices 1–3 (backend, API, console and BSS pre-provisioning), 2026-10-30. The design is in
[CONSOLE-UX-PROPOSAL-2026-10-30.md §4](CONSOLE-UX-PROPOSAL-2026-10-30.md#4-proposal-c-a-business-onboarding-workflow).

## What a sale records

| Record | Answers | Key fields |
|---|---|---|
| **Site** | Where | Address, optional map pin, `external_ref` (the BSS premise id, unique per customer) |
| **Service plan** | What | `code` (the BSS servicePlan code), config templates, a cellular profile (APN, DNN, MTU, bootstrap APN), `allowed_plmns` (MCC+MNC) |
| **Onboarding** | Which unit, for whom | Expected OUI + serial, expected ICCID / IMSI / IMEI, customer (tenant), `account_ref` (a BSS reference, no personal data), site, plan, role, commercial model |

A serial has at most one open onboarding. Expected SIM and handset identifiers are
masked in every API response and never written to the audit log (POPIA/RICA).

## States

`cmd/acs` evaluates the open onboarding of a device on every Inform, and again just
before a session closes. The decisions are a pure function
(`internal/onboarding.Evaluate`); the gateway only gathers facts and carries them out.
A single pass advances as far as the facts allow.

| State | Moves on when | Holds or fails when |
|---|---|---|
| PLANNED | The serial (and OUI, if given) informs. The device gets the customer and site of the sale. | |
| CONTACTED | The SIM matches the sale and its network is allowed, and the CPE uses its own Digest credential or a certificate | SIM not read yet: one GET of the SIM identifiers is queued. Wrong ICCID/IMSI or a network outside `allowed_plmns` → **ON_HOLD** (the reason names the network and masked identifiers) |
| SECURED | The parameter tree is discovered, if the plan needs it (always, when it has a cellular profile) | |
| DISCOVERED | Passed through in the same pass; the plan's configuration is queued | |
| CONFIGURING | Every configuration job succeeded and was read back | A job failed → **FAILED** with the fault |
| IN_SERVICE | | |

**ON_HOLD** and **FAILED** wait for an operator: *Retry* re-runs every check from
CONTACTED (or from PLANNED, if no device was bound). *Cancel* closes the onboarding;
the serial can then be sold again.

**A sale never moves a device between tenants.** Creating a sale for a serial already
in the inventory is refused (409) if that device belongs to another customer or is
outside the caller's scope. If a device gains another owner after the sale, first
contact puts the onboarding ON_HOLD without binding it, and the store refuses the
reassignment in the same transaction regardless of the caller.

The SIM read, the check and the configuration it unlocks usually finish in the first
session: when the CPE runs out of jobs, the reconciler runs again before the session
closes. Only an Inform queues a SIM read, so a read that keeps failing cannot loop a
session.

### Configuration order and the cellular profile

Templates are applied first, in plan order, then the cellular profile in its own job,
last: a wrong APN can cut the data link and, with it, the ACS. Cellular paths are
resolved against the device's discovered model (vendor fields such as
`X_ALU-COM_DNN` are found by suffix under the lowest `Device.Cellular.AccessPoint.{i}`).
A field is written only if the device has it writable and its cached value differs.
Every write uses `Verify` (read-back) and goes through the protected-parameter guard.

### Network lock

The FastMile's operator lock is a Nokia pre-config setting that cannot be written as a
list over TR-069. The ACS therefore checks instead of writing: a SIM whose IMSI is not
on an allowed MCC+MNC holds the onboarding.

## API

Reads are open to every role; changes need `tenancy.manage` (managers by default).
A scoped operator must name a customer within their scope, sees only their customers'
sites and sales, sees shared plans, and sees no unclaimed devices (these have no
customer yet).

| Method | Path | |
|---|---|---|
| GET / POST | `/api/v1/sites`, `/api/v1/sites/{id}` | |
| GET / POST | `/api/v1/service-plans`, `/api/v1/service-plans/{id}` | |
| GET / POST | `/api/v1/onboardings` (`?state=`), `/api/v1/onboardings/{id}` | POST with `expected_serial` records a sale; with `device_id` it claims a connected CPE |
| GET | `/api/v1/onboardings/unclaimed` | CPEs that informed with no onboarding |
| POST | `/api/v1/onboardings/{id}/cancel`, `/retry` | 409 from a state that does not allow it |

Example: a sale before the CPE is installed.

```bash
curl -X POST "$ACS/api/v1/onboardings" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "expected_serial": "CSC25430018F", "expected_oui": "A4FC77",
  "customer_id": "<tenant>", "account_ref": "ACC-77",
  "site_id": "<site>", "service_plan_id": "<plan>", "commercial_model": "retail"
}'
```

## Console

**Devices → Onboarding** (`/onboarding/<tab>`):

- **Needs attention** (ON_HOLD, FAILED), **In progress**, **Planned**, **Completed**, with counts. Selecting a sale shows its step-by-step timeline, the reason it is waiting or stopped, the masked SIM, and *Retry checks*, *Cancel* and *Open device*.
- **Unclaimed**: CPEs that checked in with no sale, each with *Claim*.
- **Setup → Sites** and **Service plans**: lists and create forms. The plan form checks MCC+MNC codes before sending and forces discovery when a cellular profile is set.
- **New sale** wizard: Device (serial, optional OUI, ICCID, IMSI, IMEI, role) → Who (customer, BSS account reference, commercial model) → Where (existing site of that customer, a new site, or none) → What (plan, with a summary of what it writes) → Review.

A device's **Ownership** sub-page shows its sale's timeline, or *Claim device* when it has none.
Changes need the manager role (`tenancy.manage`); other roles can look.

## BSS pre-provisioning

A sale the BSS sends before installation no longer fails for an unknown CPE.

**TMF641.** An `add` item whose `ouiSerial` the ACS has not seen records a PLANNED
onboarding (source `bss`) and the item becomes **`AWAITING_DEVICE`**. Later items in the
order wait behind it (a Wi-Fi `modify` runs only once the CPE is in service). Optional
item fields: `iccid`, `imsi`, and `place` (`id` = BSS premise reference, `name`, `street`,
`city`, `postalCode`, `country`; a site is created on first use).

```json
{"externalId": "SO-1", "accountId": "ACC-77", "orderItem": [
  {"action": "add", "role": "gateway", "ouiSerial": "A4FC77+3TG02241+CSC25430018F", "servicePlan": "HOME-5G",
   "imsi": "655060000000013", "place": {"id": "PREM-9", "name": "14 Oak Ave", "city": "Embalenhle"}},
  {"action": "modify", "role": "gateway", "parameters": {"ssid": "Oak"}}]}
```

**`POST /bss/v1/mappings`** does the same with `"pre_provision": true` and answers **202**
(`status: AWAITING_DEVICE`, `onboarding_id`, `href` and a `Location` header pointing to
the sale read, `plan_matched`). Without the flag an unknown device is still a 404, so
existing integrations are unchanged.

**Sale read.** `GET /bss/v1/onboardings/{id}` (scope `tmf:read`) shows a sale of one of
the caller's accounts: `state`, `reason_code`, `reason`, `service_plan`, `plan_matched`,
`device_id` once the CPE connected, timestamps in UTC, and `expires_at` while PLANNED.
Another customer's sale reads as 404. No SIM identifiers.

**Retry and cancel from the BSS.** `POST /bss/v1/onboardings/{id}/retry` (scope
`tmf:execute`) starts an ON_HOLD or FAILED sale again once the BSS fixed the cause (for
example sold the SIM the CPE reports); the ACS re-checks on the CPE's next contact.
`POST /bss/v1/onboardings/{id}/cancel` cancels a sale that is not in service. Both answer
with the sale, emit an `OnboardingStateChangeEvent`, and give `409 ErrSaleWrongState`
when the state does not allow it.

**Devices.** `GET /bss/v1/devices?limit=&cursor=` lists the customer's devices with their
current account, role, mapping status and reachability, in pages (`next_cursor`). `limit` is
1 to 500; a value outside that range (or none) means 100. A cursor that is not one the ACS
gave is `400 ErrInvalidRequest`.

**Ending a sale with TMF641.** A `delete` for the account and role cancels a sale that is not
in service yet (reason "deleted by service order …"); the `add` that waits for its CPE then
FAILS. A sale held with no CPE contact does not expire: only PLANNED sales do.

**What happens next** (cmd/bssadapter, every 15 s):

| Onboarding | Account mapping | Order item |
|---|---|---|
| PLANNED | none | `AWAITING_DEVICE`, note "waiting for the device to connect" |
| CONTACTED … CONFIGURING, ON_HOLD, FAILED | written `PENDING_ACTIVE` on first contact | `AWAITING_DEVICE`, note = state and reason |
| IN_SERVICE | `ACTIVE` | `COMPLETED` with the mapping; later items run |
| CANCELLED | ended (`corrected`) | `FAILED` "onboarding cancelled", with the reason |

ON_HOLD and FAILED do not fail the order: an operator fixes the cause and uses *Retry*
in the console. Cancelling the TMF641 order while its item awaits the device cancels the
sale. Retrying an order is safe: the same serial and account get the same onboarding;
another account cannot take over an open sale.

**Expiry.** A sale no CPE has contacted within **`ACS_BSS_SALE_TTL`** of its last change
(a Go duration, default `720h` = 30 days; `0` disables) is **CANCELLED** with reason code `TTL_EXPIRED`
and an `OnboardingStateChangeEvent`; its TMF641 item FAILS and later items are skipped.
The serial is then free for a new sale. A sale whose CPE connected never expires, even
when held: an operator decides.

**Which customer.** A BSS account is not an ACS customer. The **service plan code** ties
the sale to one: the shared plan with that code, else the one customer plan with it (two
customers with the same code and no shared plan is refused as ambiguous). An unknown
code is kept on the mapping but configures nothing, and is reported: `plan_matched:
false` and a `PLAN_NOT_FOUND` warning in the mapping response and the sale read. A
serial already in another customer's inventory is refused.

**Events.** Every state change of a sale is a TMF688 `OnboardingStateChangeEvent`
(`onboardingId`, `externalId`, `serialNumber`, `state`, `previousState`, `reason`,
`reasonCode`, `source`; no SIM identifiers), and a SIM, network or other-customer hold
also raises `IdentityMismatch`. Follow them on a webhook or replay them; see
[integration/09-events.md](integration/09-events.md).

## Not built yet

- Strict mode (`ACS_ONBOARDING_REQUIRE_PLAN`): unclaimed CPEs kept on the bootstrap credential.
- Bulk import of account, site and plan columns, and the `label` import bug.
- Replacement (RMA) hand-over, SIM-swap alarms after IN_SERVICE.
