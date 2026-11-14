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
export ACS_BSS_OAUTH_SIGNING_SECRET="ci-only-bss-oauth-secret-0123456789abcdef"
export ACS_BSS_API_TOKEN=""
export ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS=true
export ACS_BSS_ALLOWED_CIDRS="198.51.100.0/24, 2001:db8:51::/64"
export ACS_TRUSTED_PROXY_CIDRS="127.0.0.1/32"
export ACS_MFA_REQUIRED_ROLES="superadmin,manager,noc"
export ACS_CREDENTIAL_ENCRYPTION_KEY="ci-only-encryption-key-0123456789"
export ACS_WEBGUI_ALLOW_SAME_ORIGIN=false
export ACS_INSECURE_DEV_MODE=""

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

expect_pass "two-step sign-in required for all roles" env ACS_MFA_REQUIRED_ROLES=all bash "$PREFLIGHT"
expect_pass "role list with spaces and capitals" env ACS_MFA_REQUIRED_ROLES="SuperAdmin, manager, noc, readonly" bash "$PREFLIGHT"

expect_fail "insecure dev mode" env ACS_INSECURE_DEV_MODE=true bash "$PREFLIGHT"
expect_fail "legacy shared BSS token still set" env ACS_BSS_API_TOKEN=legacy-shared-token-0123456789 bash "$PREFLIGHT"
expect_fail "global BSS clients accepted" env ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS=false bash "$PREFLIGHT"
expect_fail "customer-bound flag unset" env ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS= bash "$PREFLIGHT"
expect_fail "no BSS OAuth signing secret" env ACS_BSS_OAUTH_SIGNING_SECRET= bash "$PREFLIGHT"
expect_fail "no BSS source networks" env ACS_BSS_ALLOWED_CIDRS= bash "$PREFLIGHT"
expect_fail "universal IPv4 BSS source network" env ACS_BSS_ALLOWED_CIDRS=0.0.0.0/0 bash "$PREFLIGHT"
expect_fail "universal IPv6 BSS source network" env ACS_BSS_ALLOWED_CIDRS=198.51.100.0/24,::/0 bash "$PREFLIGHT"
expect_fail "BSS source that is not a CIDR" env ACS_BSS_ALLOWED_CIDRS=198.51.100.7 bash "$PREFLIGHT"
expect_fail "ingress proxy not trusted" env ACS_TRUSTED_PROXY_CIDRS= bash "$PREFLIGHT"
expect_fail "no two-step sign-in requirement" env ACS_MFA_REQUIRED_ROLES= bash "$PREFLIGHT"
expect_fail "two-step sign-in for superadmin only" env ACS_MFA_REQUIRED_ROLES=superadmin bash "$PREFLIGHT"
expect_fail "two-step sign-in missing noc" env ACS_MFA_REQUIRED_ROLES=superadmin,manager bash "$PREFLIGHT"
expect_fail "no credential encryption key" env ACS_CREDENTIAL_ENCRYPTION_KEY= bash "$PREFLIGHT"
expect_fail "device web GUI sandbox disabled" env ACS_WEBGUI_ALLOW_SAME_ORIGIN=true bash "$PREFLIGHT"

# The standalone switch: an ACS that runs on its own needs none of the
# adapter's settings, but every operator-side rule still applies.
standalone=(env ACS_INTEGRATION_MODE=standalone ACS_BSS_OAUTH_SIGNING_SECRET= ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS= ACS_BSS_ALLOWED_CIDRS= ACS_TRUSTED_PROXY_CIDRS=)
expect_pass "standalone needs no BSS adapter settings" "${standalone[@]}" bash "$PREFLIGHT"
expect_pass "integration mode written with capitals" env ACS_INTEGRATION_MODE=NaaS bash "$PREFLIGHT"
expect_fail "standalone still requires two-step sign-in" "${standalone[@]}" ACS_MFA_REQUIRED_ROLES= bash "$PREFLIGHT"
expect_fail "standalone still forbids insecure dev mode" "${standalone[@]}" ACS_INSECURE_DEV_MODE=true bash "$PREFLIGHT"
expect_fail "unknown integration mode" env ACS_INTEGRATION_MODE=stand-alone bash "$PREFLIGHT"
expect_fail "naas mode without its settings" env ACS_INTEGRATION_MODE=naas ACS_BSS_ALLOWED_CIDRS= bash "$PREFLIGHT"

echo "security preflight regression matrix passed"
