#!/usr/bin/env bash
# Prove that an operator-queued job reaches an already-connected real USP
# agent and resolves with real data -- the thing the whole
# usp-job-dispatch plan exists to build (spec §9: protocol code that has
# never met a real agent is an intention, not a capability).
#
# Device lookup deliberately goes through usp_agents.endpoint_id rather
# than a predicted devices.oui_serial: obuspa's Device.DeviceInfo.
# SerialNumber is generated/persisted at first boot (only
# ManufacturerOUI/ProductClass are fixed vendor_defs.h defaults), so it
# can't be predicted or hardcoded here. usp_agents.endpoint_id ->
# device_id is the same link B-3a's probe-fallback identity
# reconciliation (handleProbeFallback -> fromProbeFallback ->
# UpsertFromOnBoard -> LinkUspAgent) already established by the time
# assert-getresp.sh (this script's caller) has confirmed the probe's
# GetResp arrived. An empty lookup here is itself meaningful: it means
# that reconciliation never ran, and this fails loudly rather than
# silently proceeding to insert a job against an empty device_id.
#
# Then: insert a real GET_PARAMETER job directly into the jobs table
# (internal/jobs/job.go's Create, reproduced here as raw SQL since this
# is a bash CI step) and poll for it to resolve via the dispatcher's
# LISTEN/NOTIFY path, asserting result_detail actually carries a
# parameter from the real agent -- concrete evidence that LISTEN/NOTIFY,
# the type mapping, and sync-completion correlation all work against a
# real agent, not just fakes.
#
# Prints the job row (and the uspc log, if given) on failure, mirroring
# assert-getresp.sh's "print evidence on failure" pattern.
set -euo pipefail

dsn="$1"; agent="$2"; uspc_log="${3:-}"

fail() {
  echo "FAIL: $1"
  if [ -n "${job_id:-}" ]; then
    echo "--- job row (id=$job_id) ---"
    psql "$dsn" -c "SELECT id, command_key, device_id, type, status, payload, attempts, fault_code, fault_string, result_detail FROM jobs WHERE id = '$job_id';" 2>/dev/null || true
  fi
  if [ -n "$uspc_log" ]; then
    echo "--- uspc log ($uspc_log) ---"
    cat "$uspc_log" 2>/dev/null || echo "(missing)"
  fi
  exit 1
}

echo "looking up device_id for endpoint $agent via usp_agents (B-3a identity reconciliation)"
# Retried, not a single lookup: handleProbeFallback's "usp probe: parameter"
# log line -- what assert-getresp.sh (this script's caller) actually
# matches on to declare success -- is written BEFORE the usp_agents row
# write it triggers has necessarily committed. A single immediate lookup
# here could race that write and fail loudly on pure timing on the very
# first real run (final review, Important Finding 3). 5 attempts, 1s
# apart, comfortably covers that window without masking a genuine
# reconciliation failure (which still fails loudly below).
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

command_key="ci-getparam-test-$(date +%s%N)"
echo "queuing GET_PARAMETER job (command_key=$command_key) for device $device_id"
job_id=$(psql "$dsn" -tAc "
  INSERT INTO jobs (id, command_key, device_id, type, status, payload, created_by)
  VALUES (gen_random_uuid(), '$command_key', '$device_id', 'GET_PARAMETER', 'QUEUED',
          '{\"paths\":[\"Device.DeviceInfo.\"]}'::jsonb, 'ci-usp-interop')
  RETURNING id;
")
# head -n1: some psql builds still print the "INSERT 0 1" command tag as
# a second line after a RETURNING clause even with -t (tuples-only) --
# verified locally against psql 18.6 -- so take only the first line, not
# just strip whitespace from the whole (possibly two-line) output.
job_id=$(echo "$job_id" | head -n1 | tr -d '[:space:]')
if [ -z "$job_id" ]; then
  fail "job insert did not return an id"
fi
echo "OK: queued job $job_id"

for _ in $(seq 1 60); do
  status=$(psql "$dsn" -tAc "SELECT status FROM jobs WHERE id = '$job_id';")

  if [ "$status" = "SUCCESS" ]; then
    value=$(psql "$dsn" -tAc "SELECT result_detail->'params'->>'Device.DeviceInfo.SoftwareVersion' FROM jobs WHERE id = '$job_id';")
    if [ -n "$value" ] && [ "$value" != "null" ]; then
      echo "OK: job $job_id dispatched via LISTEN/NOTIFY and resolved SUCCESS against $agent"
      echo "OK: result_detail carries Device.DeviceInfo.SoftwareVersion=$value"
      exit 0
    fi
    fail "job $job_id reached SUCCESS but result_detail has no Device.DeviceInfo.SoftwareVersion"
  fi

  if [ "$status" = "FAILED" ]; then
    fail "job $job_id reached FAILED"
  fi

  sleep 1
done

fail "job $job_id did not reach SUCCESS/FAILED within 60s (last status: ${status:-unknown})"
