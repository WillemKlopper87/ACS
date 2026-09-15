# Quickstart

From credentials to a completed order. Allow about 30 minutes, most of
which is waiting for a device to check in.

## Prerequisites

- The ACS base URL for your environment.
- An OAuth2 `client_id` / `client_secret`. Request one from the ACS
  operator; it is issued through the admin panel (BSS Integration →
  Onboarding/Setup) and **the secret is shown once**.
- At least one test device that has already reported in to the ACS.
  A device the ACS has never seen cannot be mapped — step 3 will
  correctly reject it.

Ask the operator for the device's `oui_serial`, in the form
`OUI+ProductClass+SerialNumber`, for example
`001349+NR5103+S230Q12345678`.

## 1. Get a token

```bash
curl -sX POST "$ACS/bss/v1/oauth/token" \
  -u "$CLIENT_ID:$CLIENT_SECRET" \
  -d 'grant_type=client_credentials'
```

```json
{ "access_token": "eyJ...", "token_type": "Bearer", "expires_in": 3600 }
```

Tokens last one hour. Cache and reuse — do not fetch one per request, or
you will meet the rate limiter quickly.

If your OAuth2 library cannot send HTTP Basic credentials, send
`client_id` and `client_secret` as form fields instead; both are
accepted.

```bash
export TOKEN="eyJ..."
```

## 2. Verify the token with a safe read

```bash
curl -s "$ACS/bss/v1/mappings/ACC-TEST-001" \
  -H "Authorization: Bearer $TOKEN"
```

An unknown account returns an empty list, not an error. A `401` here
means the token is wrong; a `429` means you are being rate limited.

## 3. Bind a device to an account

```bash
curl -sX POST "$ACS/bss/v1/mappings" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
        "account_id":   "ACC-TEST-001",
        "oui_serial":   "001349+NR5103+S230Q12345678",
        "role":         "gateway",
        "service_plan": "5G_HOME_ULTRA"
      }'
```

```json
{
  "account_id":  "ACC-TEST-001",
  "device_uuid": "65ee0038-6583-4075-b54b-246655b0fd90",
  "oui_serial":  "001349+NR5103+S230Q12345678",
  "service_plan":"5G_HOME_ULTRA",
  "status":      "ACTIVE"
}
```

**On `role`:** every assignment has one — `gateway`, `ont`, `extender`,
`stb`, `ata` or `other`. Omitting it means `gateway`, so older
integrations keep working. An account may hold **at most one active
device per role**; a second device in the same role returns `409
ErrRoleAlreadyAssigned` rather than silently displacing the first. Send
`role` explicitly from the start — it is how you address the right device
once an account has more than one.

## 4. Place an order

```bash
curl -sX POST "$ACS/bss/v1/orders" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
        "external_order_id": "ORD-QS-0001",
        "account_id":        "ACC-TEST-001",
        "role":              "gateway",
        "service_type":      "INTERNET_SERVICE",
        "action":            "MODIFY_WIFI",
        "parameters": { "wifi_ssid": "QuickstartTest" }
      }'
```

```json
{
  "order_tracking_id": "ORD-QS-0001",
  "command_key":       "setparam_20260913_aa92ae2f",
  "status":            "QUEUED",
  "timestamp":         "2026-09-13T12:56:42.740Z"
}
```

`202 Accepted`. **The device has not been changed yet.** Keep the
`command_key`.

`external_order_id` must be unique per logical order and is yours to
choose. It is the idempotency key — see step 6.

## 5. Poll until it finishes

```bash
curl -s "$ACS/bss/v1/jobs/setparam_20260913_aa92ae2f" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{
  "command_key":  "setparam_20260913_aa92ae2f",
  "device_id":    "65ee0038-6583-4075-b54b-246655b0fd90",
  "type":         "SET_PARAMETER",
  "status":       "SUCCESS",
  "created_at":   "2026-09-13T12:56:42+02:00",
  "completed_at": "2026-09-13T12:56:55+02:00"
}
```

`status` is `QUEUED` → `RPC_SENT` → `SUCCESS` or `FAILED`. On `FAILED`,
`fault_code` and `fault_string` carry what the device reported.

**How long this takes depends entirely on the device.** It applies the
change on its next check-in. A lab device on a short interval takes
seconds; a field device can take hours. If it stays `QUEUED`, the device
has not checked in yet — that is normal, not a fault.

Poll no more than every 30 seconds, and prefer webhooks (step 7).

`SUCCESS` means the ACS read the parameters back from the device and
confirmed the change. It is not merely the device's acknowledgement.

## 6. Prove that retrying is safe

Resend step 4 **unchanged**:

```json
{
  "order_tracking_id": "ORD-QS-0001",
  "command_key":       "setparam_20260913_aa92ae2f",
  "status":            "SUCCESS",
  "timestamp":         "2026-09-13T12:57:05.448Z"
}
```

Same `command_key`, current status, no second job. This is the correct
response to a timeout or an uncertain outcome: **resend the identical
order**. Never generate a new `external_order_id` to retry.

## 7. Subscribe to completions

Polling does not scale. Register a webhook once:

```bash
curl -sX POST "$ACS/bss/v1/webhooks" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
        "target_url":  "https://bss.example.com/hooks/acs",
        "event_types": ["JOB_COMPLETED"]
      }'
```

The response includes a signing secret. Store it and verify the
`X-Webhook-Signature` HMAC on every delivery — details in
`bss-integration-guide.md` §4.3.

Omit `account_id` for fleet-wide delivery, or set it to scope the
subscription to one account.

## 8. Generate a client

Stop using curl:

```bash
npx openapi-typescript backend/openapi-bssadapter.yaml -o acs-api.d.ts
```

## Next

Read [the integration model](02-integration-model.md) before writing
production code. It covers the failure paths this quickstart deliberately
avoided.
