#!/bin/bash
# Regression matrix for scripts/security-preflight.sh. Kept dependency-free so
# field-RC can exercise the production security boundary on every pull request.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFLIGHT="$ROOT/scripts/security-preflight.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

for f in cwmp.crt cwmp.key usp.crt usp.key agent-ca.crt; do
  printf 'ci-readable-placeholder\n' > "$TMP/$f"
done

export ACS_DEPLOYMENT_PROFILE=production
export ACS_TLS_CERT="$TMP/cwmp.crt"
export ACS_TLS_KEY="$TMP/cwmp.key"
export ACS_TLS_MIN_VERSION=1.2
export ACS_AUTH_ALLOW_BASIC=""
export ACS_USP_TLS_CERT="$TMP/usp.crt"
export ACS_USP_TLS_KEY="$TMP/usp.key"
export ACS_USP_CLIENT_CA_CERT="$TMP/agent-ca.crt"
export ACS_USP_ALLOWED_CIDRS="10.42.0.0/16,2001:db8:42::/64"
export ACS_USP_ALLOW_PLAINTEXT=false
export ACS_FRONTEND_BASE_URL="https://acs.example.test"
export ACS_API_PUBLIC_URL="https://acs.example.test"
export ACS_API_ADDR="127.0.0.1:8080"
export ACS_BSS_ADDR="127.0.0.1:8090"
export ACS_USP_HTTP_ADDR="127.0.0.1:8092"
export ACS_FRONTEND_BIND="127.0.0.1"
export ACS_GRAFANA_PUBLIC=0
export ACS_PROMETHEUS_PUBLIC=0

expect_pass() {
  local name="$1"
  shift
  if ! "$@" >/tmp/security-preflight-test.out 2>&1; then
    echo "FAIL: expected pass: $name" >&2
    cat /tmp/security-preflight-test.out >&2
    exit 1
  fi
  echo "PASS: $name"
}

expect_fail() {
  local name="$1"
  shift
  if "$@" >/tmp/security-preflight-test.out 2>&1; then
    echo "FAIL: expected rejection: $name" >&2
    cat /tmp/security-preflight-test.out >&2
    exit 1
  fi
  echo "PASS: rejected $name"
}

expect_pass "valid production baseline" bash "$PREFLIGHT"
expect_pass "lab compatibility profile remains usable" env ACS_DEPLOYMENT_PROFILE=lab bash "$PREFLIGHT"

expect_fail "unknown deployment profile" env ACS_DEPLOYMENT_PROFILE=prodution bash "$PREFLIGHT"
expect_fail "plaintext console public origin" env ACS_FRONTEND_BASE_URL=http://acs.example.test bash "$PREFLIGHT"
expect_fail "console origin with path" env ACS_FRONTEND_BASE_URL=https://acs.example.test/admin bash "$PREFLIGHT"
expect_fail "malformed HTTPS authority" env ACS_FRONTEND_BASE_URL=https://:443 bash "$PREFLIGHT"
expect_fail "invalid HTTPS port" env ACS_FRONTEND_BASE_URL=https://acs.example.test:notaport bash "$PREFLIGHT"
expect_fail "plaintext API public origin" env ACS_API_PUBLIC_URL=http://acs.example.test bash "$PREFLIGHT"
expect_fail "separate browser API origin without reviewed CORS" env ACS_API_PUBLIC_URL=https://api.acs.example.test bash "$PREFLIGHT"
expect_fail "public API upstream bind" env ACS_API_ADDR=0.0.0.0:8080 bash "$PREFLIGHT"
expect_fail "public BSS adapter bind" env ACS_BSS_ADDR=0.0.0.0:8090 bash "$PREFLIGHT"
expect_fail "public USP health/metrics bind" env ACS_USP_HTTP_ADDR=0.0.0.0:8092 bash "$PREFLIGHT"
expect_fail "public SPA upstream bind" env ACS_FRONTEND_BIND=0.0.0.0 bash "$PREFLIGHT"
expect_fail "public Grafana compatibility flag" env ACS_GRAFANA_PUBLIC=1 bash "$PREFLIGHT"
expect_fail "public Prometheus compatibility flag" env ACS_PROMETHEUS_PUBLIC=1 bash "$PREFLIGHT"
expect_fail "legacy CWMP TLS floor" env ACS_TLS_MIN_VERSION=1.0 bash "$PREFLIGHT"
expect_fail "USP plaintext" env ACS_USP_ALLOW_PLAINTEXT=true bash "$PREFLIGHT"
expect_fail "universal IPv4 USP CIDR" env ACS_USP_ALLOWED_CIDRS=0.0.0.0/0 bash "$PREFLIGHT"
expect_fail "universal IPv6 USP CIDR" env ACS_USP_ALLOWED_CIDRS=::/0 bash "$PREFLIGHT"
expect_fail "missing CWMP certificate" env ACS_TLS_CERT= bash "$PREFLIGHT"
expect_fail "missing USP client CA" env ACS_USP_CLIENT_CA_CERT= bash "$PREFLIGHT"

echo "security preflight regression matrix passed"
