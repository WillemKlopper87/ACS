#!/usr/bin/env bash
# Runs the repository's checks on this machine, for use when GitHub Actions is
# not available. It mirrors .github/workflows/ci.yml:
#
#   go        gofmt, go vet, go build, go test (-race when a C compiler exists)
#   migrate   clean database, second run, -check, three concurrent runners
#   spec      Redocly lint of both OpenAPI files; generated frontend types match
#   frontend  lint, typecheck and build, unit tests, npm audit
#   vuln      govulncheck at the version CI pins (downloads it once)
#   images    (--images) build the five images and scan them with Trivy
#   secrets   (--secrets) gitleaks over the working tree
#
# Not run here: the Playwright browser tests (--e2e runs them), the USP interop
# job against the obuspa reference agent, and the field-rc rehearsal.
#
# Usage: scripts/ci-local.sh [--only go,migrate,spec,frontend,vuln] [--images] [--secrets] [--e2e]
# Needs Docker (a throwaway Postgres on 127.0.0.1:55440), Go and Node.
# It never touches the lab database or ~/.acs-secrets.env.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

STAGES="go,migrate,spec,frontend,vuln"
WITH_IMAGES=0; WITH_SECRETS=0; WITH_E2E=0
while [ $# -gt 0 ]; do
  case "$1" in
    --only) STAGES="$2"; shift 2 ;;
    --images) WITH_IMAGES=1; shift ;;
    --secrets) WITH_SECRETS=1; shift ;;
    --e2e) WITH_E2E=1; shift ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done
want() { case ",$STAGES," in *",$1,"*) return 0 ;; *) return 1 ;; esac; }

# Use the toolchains CI uses, not whatever is installed: a newer or older Go
# changes the govulncheck result, and a newer Node breaks a localStorage test.
GO_VERSION="$(awk '/^go /{print $2}' backend/go.mod)"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go$GO_VERSION}"
NODE_MAJOR=22

PG_NAME="acs-ci-pg"
PG_PORT="${ACS_CI_PG_PORT:-55440}"
GOVULNCHECK_VERSION="v1.7.0"
REDOCLY_VERSION="2.51.1"
TRIVY_IMAGE="aquasec/trivy:0.70.0"
GITLEAKS_IMAGE="zricethezav/gitleaks:v8.30.0"

PASSED=(); FAILED=(); SKIPPED=()
stage() { echo; echo "=== $1"; }
ok()    { PASSED+=("$1"); echo "PASS  $1"; }
bad()   { FAILED+=("$1"); echo "FAIL  $1"; }
skip()  { SKIPPED+=("$1 ($2)"); echo "SKIP  $1: $2"; }
run()   { local name="$1"; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }

need_pg() {
  command -v docker >/dev/null 2>&1 || { echo "docker is required"; return 1; }
  if ! docker ps --format '{{.Names}}' | grep -qx "$PG_NAME"; then
    docker rm -f "$PG_NAME" >/dev/null 2>&1 || true
    docker run -d --name "$PG_NAME" -e POSTGRES_USER=acs -e POSTGRES_PASSWORD=acs -e POSTGRES_DB=acs \
      -p "127.0.0.1:$PG_PORT:5432" postgres:18-alpine >/dev/null || return 1
  fi
  for _ in $(seq 1 40); do
    docker exec "$PG_NAME" pg_isready -U acs -d acs >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "postgres did not become ready"; return 1
}
fresh_db() { docker exec "$PG_NAME" psql -U acs -d acs -qc "DROP DATABASE IF EXISTS $1 WITH (FORCE)" -c "CREATE DATABASE $1" >/dev/null; }
dsn() { echo "postgres://acs:acs@127.0.0.1:$PG_PORT/$1?sslmode=disable"; }

# ---------------------------------------------------------------- go
if want go; then
  stage "go: gofmt, vet, build, test"
  ( cd backend
    out="$(gofmt -l .)"; if [ -n "$out" ]; then echo "gofmt needed on:"; echo "$out"; exit 1; fi ) && ok "gofmt" || bad "gofmt"
  ( cd backend && go vet ./... ) && ok "go vet" || bad "go vet"
  ( cd backend && go build ./... ) && ok "go build" || bad "go build"
  if need_pg; then
    RACE=""
    if command -v gcc >/dev/null 2>&1 && [ "$(go env CGO_ENABLED)" = "1" ]; then RACE="-race"; else skip "race detector" "no C compiler on this machine"; fi
    export ACS_TEST_POSTGRES_DSN="$(dsn acs)"
    ( cd backend && go test $RACE -timeout=15m -count=1 -p 1 ./... ) && ok "go test $RACE" || bad "go test $RACE"
  else
    bad "go test (postgres unavailable)"
  fi
fi

# ---------------------------------------------------------------- migrate
if want migrate; then
  stage "migrate: clean, repeat, check, concurrent"
  if need_pg; then
    fresh_db acs_ci_migrate
    export ACS_POSTGRES_DSN="$(dsn acs_ci_migrate)"
    ( cd backend && go build -o "$ROOT/.ci-migrate" ./cmd/migrate ) || bad "build migrate"
    M="$ROOT/.ci-migrate"
    run "migrate (clean database)" "$M"
    run "migrate (second run)" "$M"
    run "migrate -check" "$M" -check
    fresh_db acs_ci_migrate
    ( "$M" & "$M" & "$M" & wait ) >/dev/null 2>&1; "$M" -check >/dev/null 2>&1 && ok "migrate (three concurrent runners)" || bad "migrate (three concurrent runners)"
    rm -f "$M" "$M.exe"
    unset ACS_POSTGRES_DSN
  else
    bad "migrate (postgres unavailable)"
  fi
fi

# ---------------------------------------------------------------- spec
if want spec; then
  stage "spec: OpenAPI lint, generated types"
  for f in openapi.yaml openapi-bssadapter.yaml; do
    ( cd frontend && npx --yes "@redocly/cli@$REDOCLY_VERSION" lint "../backend/$f" --skip-rule operation-4xx-response --skip-rule no-unused-components >/tmp/ci-local-redocly.out 2>&1
      rc=$?; tail -n 3 /tmp/ci-local-redocly.out; exit $rc ) && ok "lint $f" || bad "lint $f"
  done
  ( cd frontend && npm run generate:api >/dev/null 2>&1 && git diff --exit-code -- src/api/generated.ts >/dev/null ) && ok "generated API types match the spec" || bad "generated API types match the spec"
fi

# ---------------------------------------------------------------- frontend
if want frontend; then
  stage "frontend: install, lint, build, test, audit"
  ( cd frontend && npm ci --no-audit --no-fund --silent ) && ok "npm ci" || bad "npm ci"
  ( cd frontend && npm run lint >/dev/null 2>&1 ) && ok "lint" || bad "lint"
  ( cd frontend && npm run build >/dev/null 2>&1 ) && ok "typecheck and build" || bad "typecheck and build"
  if [ "$(node -p 'process.versions.node.split(".")[0]')" = "$NODE_MAJOR" ]; then
    ( cd frontend && npx vitest run 2>&1 | tail -n 6; exit "${PIPESTATUS[0]}" ) && ok "unit tests" || bad "unit tests"
  else
    ( cd frontend && npx --yes -p "node@$NODE_MAJOR" node ./node_modules/vitest/vitest.mjs run 2>&1 | tail -n 6; exit "${PIPESTATUS[0]}" )       && ok "unit tests (under Node $NODE_MAJOR, as CI)" || bad "unit tests (under Node $NODE_MAJOR, as CI)"
  fi
  ( cd frontend && npm audit --omit=dev 2>&1 | tail -n 2; exit "${PIPESTATUS[0]}" ) && ok "npm audit (production)" || bad "npm audit (production)"
  if [ "$WITH_E2E" = "1" ]; then
    ( cd frontend && npx playwright install chromium >/dev/null 2>&1 && npm run e2e 2>&1 | tail -n 8; exit "${PIPESTATUS[0]}" ) && ok "e2e (Playwright)" || bad "e2e (Playwright)"
  else
    skip "e2e (Playwright)" "pass --e2e to run"
  fi
fi

# ---------------------------------------------------------------- vuln
if want vuln; then
  stage "vuln: govulncheck $GOVULNCHECK_VERSION"
  ( cd backend && go run "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION" ./... 2>&1 | tail -n 12; exit "${PIPESTATUS[0]}" ) && ok "govulncheck" || bad "govulncheck"
fi

# ---------------------------------------------------------------- images
if [ "$WITH_IMAGES" = "1" ]; then
  stage "images: build and Trivy (HIGH/CRITICAL fixable)"
  command -v docker >/dev/null 2>&1 || { bad "images (docker missing)"; }
  for svc in acs api bssadapter uspc migrate; do
    docker build -q -f "backend/Dockerfile.$svc" -t "acs-$svc:ci" backend >/dev/null 2>&1 && ok "build acs-$svc" || bad "build acs-$svc"
  done
  [ -f frontend/Dockerfile ] && { docker build -q -t acs-frontend:ci frontend >/dev/null 2>&1 && ok "build acs-frontend" || bad "build acs-frontend"; }
  trivy_args=(image --quiet --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1)
  mounts=(-v /var/run/docker.sock:/var/run/docker.sock)
  if [ -f .trivyignore.yaml ]; then
    mounts+=(-v "$ROOT/.trivyignore.yaml:/.trivyignore.yaml:ro"); trivy_args+=(--ignorefile /.trivyignore.yaml)
  fi
  for img in acs-acs acs-api acs-bssadapter acs-migrate acs-frontend; do
    docker image inspect "$img:ci" >/dev/null 2>&1 || { skip "scan $img" "not built"; continue; }
    MSYS_NO_PATHCONV=1 docker run --rm "${mounts[@]}" "$TRIVY_IMAGE" "${trivy_args[@]}" "$img:ci" >/tmp/ci-local-trivy.out 2>&1       && ok "scan $img" || { tail -n 12 /tmp/ci-local-trivy.out; bad "scan $img"; }
  done
fi

# ---------------------------------------------------------------- secrets
if [ "$WITH_SECRETS" = "1" ]; then
  stage "secrets: gitleaks"
  docker run --rm -v "$ROOT:/repo" "$GITLEAKS_IMAGE" detect --source /repo --no-banner --redact >/tmp/ci-local-gitleaks.out 2>&1 \
    && ok "gitleaks" || { tail -n 12 /tmp/ci-local-gitleaks.out; bad "gitleaks"; }
fi

echo
echo "=== summary"
echo "passed:  ${#PASSED[@]}"
for s in "${SKIPPED[@]:-}"; do [ -n "$s" ] && echo "skipped: $s"; done
if [ ${#FAILED[@]} -gt 0 ]; then
  echo "FAILED:  ${#FAILED[@]}"; for s in "${FAILED[@]}"; do echo "  - $s"; done
  exit 1
fi
echo "All selected checks passed."
