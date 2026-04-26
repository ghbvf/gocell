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
set -euo pipefail

base="${BASE_URL:-http://localhost:8080}"
username="${E2E_ADMIN_USERNAME:-admin}"
email="${E2E_ADMIN_EMAIL:-admin@e2e.local}"
password="${E2E_ADMIN_PASSWORD:-E2E-Bootstrap-Pwd-1!}"

curl -fsS -X POST "$base/api/v1/access/setup/admin" \
    -H "Content-Type: application/json" \
    -d "$(printf '{"username":"%s","email":"%s","password":"%s"}' "$username" "$email" "$password")" \
    >/dev/null

token=$(curl -fsS -X POST "$base/api/v1/access/sessions/login" \
    -H "Content-Type: application/json" \
    -d "$(printf '{"username":"%s","password":"%s"}' "$username" "$password")" \
    | jq -r '.data.accessToken // empty')

if [ -z "$token" ]; then
    echo "bootstrap-admin: login response missing data.accessToken" >&2
    exit 1
fi

printf 'E2E_ADMIN_TOKEN=%s\n' "$token"
