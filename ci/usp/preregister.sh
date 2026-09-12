#!/usr/bin/env bash
# preregister.sh extracts a running obuspa instance's real identity via its
# own "-c get" CLI (the out-of-band-oracle pattern assert-subscription.sh
# established), escapes it with the exact algorithm
# backend/internal/cwmp/types.go's DeviceID.NaturalKey() uses, and inserts
# it into devices with the same column shape as
# backend/internal/devices/repository.go's PreRegister (ON CONFLICT DO
# NOTHING -- a later interop step reusing the same Postgres service must
# not fail if an earlier step's obuspa instance already happened to
# register under the same OUI+ProductClass+SerialNumber).
#
# This logic used to be copy-pasted, byte-identical apart from the
# container name, into all three usp-interop steps in ci.yml (~45 lines
# each: three get_param()/esc_natural_key()/sql_escape() definitions and
# three INSERT blocks). That was judged a maintenance hazard in the Task 5
# review -- see task-5-report.md -- so it now lives here once, and every
# step calls this script instead of carrying its own copy.
#
# The WebSocket step additionally needs to assert, using this SAME
# extracted oui_serial, that cmd/uspc already refused this identity before
# it existed in devices (assert-allowlist.sh) -- that assertion must run
# strictly after extraction but before the INSERT below (run afterward,
# its "no devices row exists yet" check would trivially fail against the
# row this script just created). Rather than re-duplicating extraction to
# get that ordering, any extra arguments after <dsn> are treated as a
# pre-insert command: run once identity is known, with the computed
# oui_serial appended as its final argument, and its exit code propagated
# (INSERT is skipped if it fails).
#
# Usage: preregister.sh <obuspa_container_name> <postgres_dsn> [pre_insert_cmd...]
#   e.g. preregister.sh obuspa-mqtt5 "$DSN"
#        preregister.sh obuspa-ws "$DSN" ci/usp/assert-allowlist.sh /tmp/uspc-ws.log "$DSN" "$AGENT_ENDPOINT_ID"
#          (assert-allowlist.sh then runs as:
#           assert-allowlist.sh /tmp/uspc-ws.log "$DSN" "$AGENT_ENDPOINT_ID" "$oui_serial")

set -euo pipefail

CONTAINER="${1:?usage: preregister.sh <obuspa_container_name> <postgres_dsn> [pre_insert_cmd...]}"
POSTGRES_DSN="${2:?usage: preregister.sh <obuspa_container_name> <postgres_dsn> [pre_insert_cmd...]}"
shift 2
PRE_INSERT_CMD=("$@")

get_param() {
  local path="$1" out="" value=""
  for _ in $(seq 1 15); do
    out=$(docker exec "$CONTAINER" obuspa -c get "$path" 2>&1 || true)
    value=$(echo "$out" | sed -n 's/.*=> //p' | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' | tr -d '\r')
    if [ -n "$value" ]; then
      echo "$value"
      return 0
    fi
    sleep 1
  done
  echo "FAIL: obuspa -c get $path never returned a value -- last output: $out" >&2
  return 1
}

# Mirrors backend/internal/cwmp/types.go's DeviceID.NaturalKey() byte for
# byte: escape \ then + (order matters), per component.
esc_natural_key() {
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/+/\\+/g'
}

sql_escape() { printf '%s' "$1" | sed "s/'/''/g"; }

oui=$(get_param "Device.DeviceInfo.ManufacturerOUI")
product_class=$(get_param "Device.DeviceInfo.ProductClass")
serial_number=$(get_param "Device.DeviceInfo.SerialNumber")
echo "OK: $CONTAINER's real identity = OUI=$oui ProductClass=$product_class SerialNumber=$serial_number"

if [ -n "$product_class" ]; then
  oui_serial="$(esc_natural_key "$oui")+$(esc_natural_key "$product_class")+$(esc_natural_key "$serial_number")"
else
  oui_serial="$(esc_natural_key "$oui")+$(esc_natural_key "$serial_number")"
fi

if [ "${#PRE_INSERT_CMD[@]}" -gt 0 ]; then
  "${PRE_INSERT_CMD[@]}" "$oui_serial"
fi

echo "pre-registering $CONTAINER's real identity (oui_serial=$oui_serial) -- required since cmd/uspc's identity gate now refuses an unknown OUI+ProductClass+SerialNumber"
psql "$POSTGRES_DSN" -c "
  INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number, online_status, first_seen_at, last_updated_at, customer_id, tags)
  VALUES (gen_random_uuid(), '$(sql_escape "$oui_serial")', 'obuspa-ci', '$(sql_escape "$oui")', '$(sql_escape "$product_class")', '$(sql_escape "$serial_number")', 'OFFLINE', now(), now(), NULL, '{}')
  ON CONFLICT (oui_serial) DO NOTHING;
"

echo "preregister.sh: PASS"
