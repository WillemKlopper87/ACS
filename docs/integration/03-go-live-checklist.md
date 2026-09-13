# Go-live checklist

Work through this before production traffic. Each item corresponds to a
failure we have either seen or specifically designed against.

## Credentials and transport

- [ ] Using **OAuth2 client-credentials**, not the legacy shared token.
      The shared token is deprecated, is not per-integration, and cannot
      be rotated without a service restart.
- [ ] `client_secret` held in a secret manager. Not in config files, not
      in source control, not in CI logs.
- [ ] Token cached for its full hour and refreshed on expiry — not
      fetched per request.
- [ ] A token refresh failure raises an alert rather than silently
      retrying forever.
- [ ] You know who to contact to revoke a client, and have confirmed the
      procedure works. Revocation takes effect within ~15 seconds.
- [ ] If mTLS is required in your environment: certificates issued,
      installed, and renewal diarised **before** expiry.

## Correctness

- [ ] `external_order_id` is generated from a stable business key and
      **persisted before the API call**, so a retry after your own crash
      reuses the same value.
- [ ] Retry means **byte-identical resubmission**. No suffixes, no
      timestamps, no `-retry-2`.
- [ ] `role` is sent explicitly on every mapping and every order.
- [ ] `202 Accepted` is modelled as pending, never as success.
- [ ] Order state in your system has a genuine *unknown* state, distinct
      from *failed*, for timeouts.
- [ ] Errors are classified: only `429`, `502` and `5xx` are retried.
      `400`, `401`, `404` and `409` are not.
- [ ] Code switches on the `error` field, not on `message` text.

## Throughput

- [ ] Exponential backoff **with jitter** on every retry path.
- [ ] Peak request rate measured and within your bucket (default 5 rps,
      burst 10). Confirm your limits with the operator.
- [ ] Bulk operations paced, not fired in parallel.
- [ ] Polling, if used at all, no more frequent than every 30 seconds per
      job.
- [ ] Request bodies under 1 MiB.

## Webhooks

- [ ] Endpoint reachable from the ACS and TLS-terminated.
- [ ] **HMAC signature verified on every delivery**, before any action.
      Unverified, the endpoint lets anyone tell your BSS an order
      succeeded.
- [ ] Handler is idempotent — delivery is at-least-once.
- [ ] Returns 2xx quickly; work happens asynchronously.
- [ ] No assumption about delivery ordering.
- [ ] A reconciliation sweep polls any order non-terminal beyond a
      threshold, so a missed delivery does not strand it.

## Operations

- [ ] `command_key` and `external_order_id` recorded against every order
      in your system, and both appear in your logs. They are what make an
      issue traceable on our side.
- [ ] `wifi_password` redacted from your logs, traces and error reports.
- [ ] Dead-lettered orders are surfaced to a human rather than retried
      indefinitely.
- [ ] Alerting on: token refresh failures, sustained `429`, any `500`,
      and orders pending beyond your expected device check-in window.
- [ ] Runbook entry for "order stuck in QUEUED" — usually the device has
      not checked in, which is normal, not a fault.

## Verified against a real environment

- [ ] Full happy path exercised end to end against real hardware, not
      only mocks.
- [ ] Duplicate `external_order_id` confirmed to return the original job.
- [ ] A `429` deliberately provoked and backoff confirmed.
- [ ] Behaviour with a **powered-off device** observed: the order stays
      `QUEUED`. Confirm your system tolerates that for hours.
- [ ] A failed order observed, with `fault_code`/`fault_string` surfaced
      usefully to whoever must act on it.
- [ ] Webhook signature verification tested with a **deliberately wrong**
      signature, and confirmed rejected.

## Agreed with the operator

- [ ] Rate limits for your integration.
- [ ] Expected device check-in interval in this fleet — it determines
      realistic completion times.
- [ ] Escalation path and hours.
- [ ] Whether `SUSPEND`/`ACTIVATE` is needed. If so, raise it now: it
      requires a per-vendor walled-garden decision before it can be built
      safely, and that is a lead-time item.
