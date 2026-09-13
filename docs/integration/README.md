# ACS Integration Pack — start here

**For BSS and OSS developers integrating with the ACS platform.**

Version 2026-09-13 · covers the `/bss/v1` API, which is live and stable.

The ACS platform manages customer premises equipment (routers, ONTs,
extenders, set-top boxes) over TR-069/CWMP and USP/TR-369. It does not
hold customer, billing or product data — your BSS remains the system of
record for all of that. This API is how your BSS tells the ACS which
device belongs to which account, and asks it to change device settings.

## Read in this order

| # | Document | Why |
|---|---|---|
| 1 | [Quickstart](01-quickstart.md) | Credentials to first successful call. Start here even if you skim everything else. |
| 2 | [Integration model](02-integration-model.md) | Async execution, idempotency, retries, error handling. **The document that prevents most integration defects.** Do not skip it. |
| 3 | [`bss-integration-guide.md`](../../bss-integration-guide.md) | The workflow reference: every endpoint, request and response, with captured examples. |
| 4 | [Go-live checklist](03-go-live-checklist.md) | What to verify before production traffic. |
| 5 | [Roadmap](04-roadmap.md) | What is coming, so you can plan rather than rework. |

## Machine-readable specification

[`backend/openapi-bssadapter.yaml`](../../backend/openapi-bssadapter.yaml) —
OpenAPI 3.0.3, covering every `/bss/v1` endpoint.

Generate a client rather than hand-writing one:

```bash
# Go
oapi-codegen -package acsclient openapi-bssadapter.yaml > acsclient.go

# TypeScript
npx openapi-typescript openapi-bssadapter.yaml -o acs-api.d.ts

# Java / Python / C#
openapi-generator-cli generate -i openapi-bssadapter.yaml -g <lang> -o ./client
```

The specification is kept honest by an automated test that fails the
build if a route exists without a matching spec entry, so it does not
drift from the running service.

## What you can do today

| Capability | Endpoint |
|---|---|
| Obtain an access token | `POST /bss/v1/oauth/token` |
| Bind a device to an account | `POST /bss/v1/mappings` |
| List an account's devices | `GET /bss/v1/mappings/{account_id}` |
| Change Wi-Fi settings | `POST /bss/v1/orders` |
| Check an order's progress | `GET /bss/v1/jobs/{command_key}` |
| Subscribe to completion events | `POST /bss/v1/webhooks` |

## What you cannot do yet

- **Suspend / reactivate a service.** Deliberately unimplemented — see
  [Roadmap](04-roadmap.md) and the guide's §2.2 for why a naive
  implementation is unsafe.
- **Actions other than Wi-Fi changes.** Firmware, reboot and diagnostics
  exist in the ACS but are not yet exposed on this API.
- **Read a Wi-Fi password back.** Passwords are write-only by design.

## Three things that catch people out

1. **Nothing is synchronous.** A write returns `202 Accepted` with a
   `command_key`. The device may be asleep, behind carrier NAT, or
   powered off; completion can be seconds or hours away. Never treat
   `202` as success.
2. **Retrying is safe, and is the intended behaviour.** Resubmit the same
   `external_order_id` and you get the original job back with its current
   status — never a duplicate.
3. **`429` means slow down, not "authentication failed".** Back off and
   retry.

All three are covered properly in the [integration model](02-integration-model.md).

## Support

Report integration issues with the `command_key` or `external_order_id`,
the timestamp, and the full response body. Those three make almost any
problem traceable on our side.
