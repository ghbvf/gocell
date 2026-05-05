#!/usr/bin/env bash
# Provision the first admin user and mint an access token. Emits
#
#     E2E_ADMIN_TOKEN=<jwt>
#
# on stdout — consume via `eval` (local) or `>> "$GITHUB_ENV"` (CI). The
# token feeds the e2e suite's e2eAdminToken() helper. Failures (4xx/5xx,
# missing token in response) are fatal; there is no soft pre-check on
# /setup/status because every CI run starts from a fresh `down -v` volume.
#
# Requires jq for token extraction (preinstalled on ubuntu-latest runners).
#
# After PR #392 (auth.bootstrap closed contract, ADR §D1 + §D9) the
# /setup/admin endpoint is protected by HTTP Basic Auth using the bootstrap
# operator credentials shared with the corebundle container. Both this
# script and tests/e2e/docker-compose.e2e.yaml pull the credentials from
# tests/e2e/scripts/bootstrap-credentials.env (single source of truth) so a
# future rotation only touches one file.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./bootstrap-credentials.env
source "${script_dir}/bootstrap-credentials.env"

base="${BASE_URL:-http://localhost:8080}"
username="${E2E_ADMIN_USERNAME:-admin}"
email="${E2E_ADMIN_EMAIL:-admin@e2e.local}"
password="${E2E_ADMIN_PASSWORD:-E2E-Bootstrap-Pwd-1!}"

setup_body=$(jq -n --arg u "$username" --arg e "$email" --arg p "$password" \
    '{username:$u, email:$e, password:$p}')
curl -fsS -X POST "$base/api/v1/access/setup/admin" \
    -u "${GOCELL_BOOTSTRAP_ADMIN_USERNAME}:${GOCELL_BOOTSTRAP_ADMIN_PASSWORD}" \
    -H "Content-Type: application/json" \
    -d "$setup_body" \
    >/dev/null

login_body=$(jq -n --arg u "$username" --arg p "$password" '{username:$u, password:$p}')
token=$(curl -fsS -X POST "$base/api/v1/access/sessions/login" \
    -H "Content-Type: application/json" \
    -d "$login_body" \
    | jq -r '.data.accessToken // empty')

if [[ -z "$token" ]]; then
    echo "bootstrap-admin: login response missing data.accessToken" >&2
    exit 1
fi

printf 'E2E_ADMIN_TOKEN=%s\n' "$token"
