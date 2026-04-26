#!/usr/bin/env bash
# Provision the first admin user and mint an access token. Emits
#
#     E2E_ADMIN_TOKEN=<jwt>
#
# on stdout — consume via `eval` (local) or `>> "$GITHUB_ENV"` (CI). The
# token feeds the e2e suite's e2eAdminToken() helper.
#
# Retry policy: compose `--wait` plus the corebundle /readyz healthcheck
# already gate on cell readiness, but transient TCP errors (port-publish
# race after `up`, runner network jitter) and brief 5xx are still possible.
# We attempt each call up to retry_max times with a fixed backoff, retrying
# only on curl transport errors and 5xx; any 4xx fails fast (a 4xx here
# means a contract regression, not a transient).
#
# `down -v` between CI runs guarantees clean state, so /setup/admin is
# expected to succeed exactly once per run; no soft 410 tolerance.
#
# Requires jq for token extraction (preinstalled on ubuntu-latest runners).
set -euo pipefail

base="${BASE_URL:-http://localhost:8080}"
username="${E2E_ADMIN_USERNAME:-admin}"
email="${E2E_ADMIN_EMAIL:-admin@e2e.local}"
password="${E2E_ADMIN_PASSWORD:-E2E-Bootstrap-Pwd-1!}"
retry_max="${BOOTSTRAP_RETRY_MAX:-5}"
retry_sleep="${BOOTSTRAP_RETRY_SLEEP:-2}"

# post_with_retry POST <url> <body-json>. Retries on curl exit (network) or
# HTTP 5xx; fast-fails on 2xx success or 4xx contract error. Echoes response
# body on success.
post_with_retry() {
    local url="$1" body="$2" attempt=1 status response
    while [ "$attempt" -le "$retry_max" ]; do
        response=$(curl -sS -o /tmp/bootstrap.body -w '%{http_code}' \
            -X POST "$url" \
            -H "Content-Type: application/json" \
            -d "$body" || echo "000")
        status="$response"
        if [ "$status" = "201" ] || [ "$status" = "200" ]; then
            cat /tmp/bootstrap.body
            return 0
        fi
        if [ "$status" -ge 400 ] && [ "$status" -lt 500 ] 2>/dev/null; then
            echo "bootstrap-admin: $url returned $status (4xx, not retried)" >&2
            cat /tmp/bootstrap.body >&2
            return 1
        fi
        echo "bootstrap-admin: $url attempt $attempt/$retry_max failed (status=$status), retrying in ${retry_sleep}s" >&2
        attempt=$((attempt + 1))
        sleep "$retry_sleep"
    done
    echo "bootstrap-admin: $url exhausted $retry_max retries" >&2
    return 1
}

setup_body=$(jq -n --arg u "$username" --arg e "$email" --arg p "$password" \
    '{username:$u, email:$e, password:$p}')
post_with_retry "$base/api/v1/access/setup/admin" "$setup_body" >/dev/null

login_body=$(jq -n --arg u "$username" --arg p "$password" '{username:$u, password:$p}')
login_resp=$(post_with_retry "$base/api/v1/access/sessions/login" "$login_body")
token=$(printf '%s' "$login_resp" | jq -r '.data.accessToken // empty')

if [ -z "$token" ]; then
    echo "bootstrap-admin: login response missing data.accessToken" >&2
    exit 1
fi

printf 'E2E_ADMIN_TOKEN=%s\n' "$token"
