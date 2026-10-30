# Onboarding: from sale to service

Status: slice 1 (backend and API), 2026-10-30. The console screens (slice 2) and BSS
pre-provisioning (slice 3) are not built yet. The design is in
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

## Not in slice 1

- Console: the Onboarding screen (Planned, In progress, Unclaimed, Completed/failed), the wizard and the timeline on the device Overview.
- BSS: TMF641 `add` and `POST /bss/v1/mappings` creating PLANNED onboardings, `PENDING_ACTIVE` → `ACTIVE`, `ONBOARDING_STATE_CHANGED` webhooks and TMF688 events.
- Strict mode (`ACS_ONBOARDING_REQUIRE_PLAN`): unclaimed CPEs kept on the bootstrap credential.
- Bulk import of account, site and plan columns, and the `label` import bug.
- Expiry of PLANNED records, replacement (RMA) hand-over, SIM-swap alarms after IN_SERVICE.
