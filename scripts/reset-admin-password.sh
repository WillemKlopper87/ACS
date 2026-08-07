#!/bin/bash
# Forces the operators.admin row's password to match whatever's
# currently in ~/.acs-secrets.env — for the situation where Postgres
# data has persisted across a credential regeneration. cmd/api's
# bootstrapAdmin only ever creates the admin account once, the moment
# the operators table is empty (auth_handlers.go) — every credential
# set gen-env.sh prints after that point is real and saved, but never
# actually applied, since the table was never empty again. This closes
# that gap directly rather than requiring a full DB wipe.
set -e

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/gen-env.sh"

echo "Hashing password for $ACS_BOOTSTRAP_ADMIN_USERNAME..."
cd "$ROOT/backend"
HASH="$(go run ./cmd/hashpw "$ACS_BOOTSTRAP_ADMIN_PASSWORD")"

echo "Updating operators.password_hash in Postgres..."
UPDATED=$(docker exec infra-postgres-1 psql -U acs -d acs -t -A -c \
  "UPDATE operators SET password_hash = '$HASH' WHERE username = '$ACS_BOOTSTRAP_ADMIN_USERNAME' RETURNING username;")

if [ -z "$UPDATED" ]; then
  echo "No operator named '$ACS_BOOTSTRAP_ADMIN_USERNAME' exists yet — creating one instead."
  docker exec infra-postgres-1 psql -U acs -d acs -c \
    "INSERT INTO operators (id, username, email, password_hash, role) VALUES (gen_random_uuid(), '$ACS_BOOTSTRAP_ADMIN_USERNAME', NULL, '$HASH', 'superadmin');"
fi

echo ""
echo "Done. Log in with:"
echo "  Username: $ACS_BOOTSTRAP_ADMIN_USERNAME"
echo "  Password: $ACS_BOOTSTRAP_ADMIN_PASSWORD"
