#!/usr/bin/env bash
# Prove that a usp_subscriptions row (Task 3's desired-state table) actually
# converges onto a real obuspa agent as a Device.LocalAgent.Subscription.{i}.
# instance, and that a resulting ValueChange Notify gets routed and cached --
# the last unproven piece of the USP subscription-reconciliation work
# (B-3c, task-7 brief). Two things are asserted, in order:
#
#  1. Reconciliation. Insert a usp_subscriptions row for the already-
#     connected test device (ValueChange on a real, controller-writable
#     TR-181 parameter -- Device.LocalAgent.Controller.1.PeriodicNotifInterval,
#     deliberately NOT Device.DeviceInfo.SoftwareVersion, which is
#     agent-owned and not controller-writable), then force a genuine
#     reconnect (docker restart the obuspa container). cmd/uspc's only
#     production trigger for subscriptionReconciler.reconcile is the
#     on-connect hook (handler.go's resolveAndMarkReconciled, task-6) -- there
#     is no manual-trigger code path, by design -- so proving reconciliation
#     against a row inserted after the first connect genuinely requires a
#     second connect, not a simulated one. Because /tmp/usp-ws.db already
#     exists inside the container's filesystem from the first boot,
#     restarting (rather than recreating) the container does NOT reapply the
#     factory-reset file (design spec S9: "the agent database must be removed
#     between runs or the factory-reset file is ignored") -- exactly the
#     "subscription reconciliation across an agent restart" coverage S9 asks
#     for, and it preserves the already-established controller trust config
#     from the first connect.
#
#     Evidence: cmd/uspc's own log line "subscription reconciler: Add
#     succeeded" (cmd/uspc/subscriptions.go's logAddResult), which only
#     prints once a real AddResp -- a message obuspa itself sent back --
#     reports OperSuccess; AND, independently, obuspa's own "-c" CLI (design
#     spec S3.4/S9: "obuspa's -c CLI asserts device-side state independently
#     of our controller"), invoked directly inside the container via
#     `docker exec`, confirms the agent's own Device.LocalAgent.Subscription.
#     table actually carries this subscription's ID/NotifType/ReferenceList.
#     Two independent oracles, not just uspc's own opinion of what happened.
#
#  2. Routed Notify. Force the referenced parameter to a new value via
#     obuspa's own "-c set" CLI command -- a CI-only trick, not a new
#     production code path: it writes through the same data-model layer a
#     real over-the-wire USP Set would, so the agent's own periodic
#     ValueChange-poll thread (VALUE_CHANGE_POLL_PERIOD = 15s in obuspa's
#     src/vendor/vendor_defs.h at the pinned commit) detects and Notifies it
#     exactly as it would for a change from any other source. Then poll
#     device_parameter_cache directly for the new value landing with
#     source = 'USP_NOTIFY_VALUE_CHANGE' (internal/parameters/cache.go's
#     SourceUSPNotify, verified against that file, not guessed) -- proof the
#     Notify was decoded, routed, and cached, not just that obuspa sent
#     something.
#
# Mirrors assert-job-dispatch.sh's shape throughout: a retried usp_agents
# lookup, a raw psql insert once the device id is known, polling loops, and
# "print evidence on failure".
set -euo pipefail

dsn="$1"; agent="$2"; uspc_log="$3"; container="$4"

fail() {
  echo "FAIL: $1"
  echo "--- uspc log ($uspc_log) ---"
  cat "$uspc_log" 2>/dev/null || echo "(missing)"
  echo "--- obuspa container logs ($container, last 200 lines) ---"
  docker logs --tail 200 "$container" 2>&1 || true
  exit 1
}

echo "looking up device_id for endpoint $agent via usp_agents (B-3a identity reconciliation)"
# Same 5-retry pattern as assert-job-dispatch.sh, for the same reason: this
# script's caller only guarantees assert-getresp.sh's probe evidence has
# landed, not that the usp_agents row it triggers has committed yet.
device_id=""
for _ in $(seq 1 5); do
  device_id=$(psql "$dsn" -tAc "SELECT device_id FROM usp_agents WHERE endpoint_id = '$agent';")
  if [ -n "$device_id" ]; then
    break
  fi
  sleep 1
done
if [ -z "$device_id" ]; then
  fail "no usp_agents row for endpoint_id=$agent -- identity reconciliation did not link this agent to a device"
fi
echo "OK: endpoint $agent -> device_id $device_id"

# Device.LocalAgent.Controller.1.PeriodicNotifInterval, not
# Device.DeviceInfo.SoftwareVersion: SoftwareVersion is agent-owned and not
# controller-writable (task-7 brief). PeriodicNotifInterval is a real,
# controller-writable (Access=ReadWrite in the USP LocalAgent data model)
# TR-181 parameter this same factory-reset file already sets to "86400"
# (ci/usp/obuspa-websocket.txt) -- proof obuspa implements it -- and its
# path is static (Controller.1 is fixed by that same file), so it can be
# named here without first discovering a dynamic instance number.
watched_param="Device.LocalAgent.Controller.1.PeriodicNotifInterval"
new_value="43200"

echo "inserting usp_subscriptions row for device $device_id: ValueChange on $watched_param"
# Same gen_random_uuid()-then-RETURNING-then-head-n1 pattern as
# assert-job-dispatch.sh's job insert (that script's own comment: some psql
# builds print a second "INSERT 0 1" command-tag line even in -t mode).
sub_id=$(psql "$dsn" -tAc "
  INSERT INTO usp_subscriptions (id, device_id, notif_type, reference_list, persistent, created_by)
  VALUES (gen_random_uuid(), '$device_id', 'ValueChange', ARRAY['$watched_param']::text[], false, 'ci-usp-interop')
  RETURNING id;
")
sub_id=$(echo "$sub_id" | head -n1 | tr -d '[:space:]')
if [ -z "$sub_id" ]; then
  fail "usp_subscriptions insert did not return an id"
fi
echo "OK: usp_subscriptions row inserted (id=$sub_id)"

echo "forcing a genuine reconnect (docker restart $container) -- reconcile() only ever runs from resolveAndMarkReconciled, the on-connect hook (task-6); there is no manual-trigger path, so a fresh connection is the only real way to exercise it against a row inserted after the first connect"
docker restart "$container" >/dev/null

echo "waiting for reconciliation to Add the new subscription (uspc log: 'subscription reconciler: Add succeeded', subscription_id=$sub_id)"
added=0
for _ in $(seq 1 60); do
  if [ -f "$uspc_log" ]; then
    matches=$(grep 'msg="uspc: subscription reconciler: Add succeeded"' "$uspc_log" 2>/dev/null \
      | grep "device_id=$device_id" \
      | grep "subscription_id=$sub_id" || true)
    if [ -n "$matches" ]; then
      added=1
      break
    fi
  fi
  sleep 1
done
if [ "$added" -ne 1 ]; then
  fail "no 'subscription reconciler: Add succeeded' log line for subscription_id=$sub_id within 60s of the forced reconnect"
fi
echo "OK: uspc's own log confirms the Add succeeded (a real AddResp from obuspa)"
echo "$matches" | tail -3

echo "independently confirming via obuspa's own -c CLI (design spec S3.4/S9's out-of-band oracle) that the agent's Device.LocalAgent.Subscription. table actually carries this subscription"
cli_ok=0
cli_out=""
for _ in $(seq 1 10); do
  cli_out=$(docker exec "$container" obuspa -c get "Device.LocalAgent.Subscription." 2>&1 || true)
  if echo "$cli_out" | grep -q ".ID => $sub_id" \
     && echo "$cli_out" | grep -q ".NotifType => ValueChange" \
     && echo "$cli_out" | grep -q ".ReferenceList => $watched_param"; then
    cli_ok=1
    break
  fi
  sleep 1
done
if [ "$cli_ok" -ne 1 ]; then
  fail "obuspa's own -c CLI does not show a Device.LocalAgent.Subscription. instance for id=$sub_id -- last output:
$cli_out"
fi
echo "OK: obuspa's own -c CLI (independent of cmd/uspc) confirms the instance exists on the real agent"
echo "$cli_out" | grep -E '\.(ID|NotifType|ReferenceList) => '

# Everything above only ever exercised actualSubscriptionItems' decode of a
# GetResp with ZERO existing instances (the device started empty). Force a
# second genuine reconnect now that one real instance exists, so this
# second reconcile pass decodes a real NON-EMPTY GetResp against a real
# agent -- proving the instance is recognized as already-converged (not
# deleted-then-recreated, not duplicated), not just asserted by hand-built
# Go test fixtures (final-review finding 5).
echo "forcing a second reconnect (docker restart $container) to exercise the decode path against a real non-empty GetResp"
docker restart "$container" >/dev/null

echo "waiting for the second reconcile pass to settle, then asserting exactly ONE Device.LocalAgent.Subscription. instance survives (not zero: not deleted-then-recreated; not two: not duplicated)"
settled=0
for _ in $(seq 1 60); do
  cli_out2=$(docker exec "$container" obuspa -c get "Device.LocalAgent.Subscription." 2>&1 || true)
  total=$(echo "$cli_out2" | grep -c '\.ID => ' || true)
  if [ "$total" -eq 1 ] && echo "$cli_out2" | grep -q ".ID => $sub_id"; then
    settled=1
    break
  fi
  sleep 1
done
if [ "$settled" -ne 1 ]; then
  fail "after a second reconnect, obuspa's -c CLI does not show exactly one Device.LocalAgent.Subscription. instance for id=$sub_id -- last output:
$cli_out2"
fi
echo "OK: exactly one instance survives the second reconcile pass"
if grep 'msg="uspc: subscription reconciler: Add failed"' "$uspc_log" 2>/dev/null | grep -q "subscription_id=$sub_id"; then
  fail "cmd/uspc logged an Add failure for subscription_id=$sub_id after the second reconnect"
fi
echo "OK: no Add failure logged for subscription_id=$sub_id after the second reconnect"

echo "forcing a ValueChange via obuspa's own -c set CLI (a CI-only trick, not a new production trigger -- see this script's header comment): $watched_param -> $new_value"
set_out=$(docker exec "$container" obuspa -c set "$watched_param" "$new_value" 2>&1 || true)
echo "$set_out"
if echo "$set_out" | grep -qiE "No objects were matched|not permitted to be set|^ERROR"; then
  fail "obuspa's -c set rejected the change to $watched_param"
fi

echo "polling device_parameter_cache for the routed ValueChange (source = USP_NOTIFY_VALUE_CHANGE, internal/parameters/cache.go's SourceUSPNotify)"
value=""
source=""
for _ in $(seq 1 90); do
  row=$(psql "$dsn" -tAc "
    SELECT COALESCE(parameters->'$watched_param'->>'value', '') || '|' || COALESCE(parameters->'$watched_param'->>'source', '')
    FROM device_parameter_cache WHERE device_id = '$device_id';
  ")
  value="${row%%|*}"
  source="${row##*|}"
  if [ "$value" = "$new_value" ] && [ "$source" = "USP_NOTIFY_VALUE_CHANGE" ]; then
    echo "OK: $watched_param = $value cached with source=$source -- the ValueChange Notify was decoded, routed, and cached"
    exit 0
  fi
  sleep 1
done

fail "device_parameter_cache never showed $watched_param=$new_value with source=USP_NOTIFY_VALUE_CHANGE within 90s (last observed: value='$value' source='$source')"
