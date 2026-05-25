#!/usr/bin/env bash
# healthcheck-verify.sh
# Boots all Docker Compose services, waits up to TIMEOUT seconds for every
# service to pass its built-in health check, prints status, then tears down.
#
# INVARIANT (HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01, issue #19):
#   - `trap ... EXIT` must be installed before the first `docker compose`
#     call so cleanup runs on success, `set -e` early-exit, and SIGINT.
#   - `docker compose up --wait` must be bounded by `--wait-timeout`,
#     never bare `--timeout` (that's the stop-shutdown flag and is
#     silently ignored as a wait bound).
# Both are enforced by tools/archtest/healthcheck_verify_script_test.go.
#
# Exit 0 on success, non-zero if any service fails within the timeout.

set -euo pipefail

TIMEOUT=30

trap 'docker compose down' EXIT

echo "Starting Docker Compose services..."
docker compose up -d --wait --wait-timeout "${TIMEOUT}"

echo ""
echo "All services healthy."
echo ""
docker compose ps

echo ""
echo "Healthcheck verification complete."
