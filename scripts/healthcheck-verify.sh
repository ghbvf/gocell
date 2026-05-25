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

# Cleanup preserves the original exit code: bash's EXIT trap would
# otherwise overwrite $? with the trap body's exit code, hiding the
# real failure from callers (CI scripts, make targets). The explicit
# `exit "$_rc"` at the end restores the caller-visible code while the
# `|| ...` branch ensures a noisy `down` failure is logged, not
# silenced (a silently-failed cleanup leaves orphan containers and
# breaks the next run with port conflicts).
#
# shellcheck disable=SC2154
# _rc is assigned inside the same trap body string before it is
# referenced; shellcheck cannot follow sequence inside a single-quoted
# trap argument.
trap '_rc=$?; echo "[healthcheck-verify] tearing down containers..." >&2; docker compose down || echo "[healthcheck-verify] WARNING: docker compose down exited $? — orphan containers may remain" >&2; exit "$_rc"' EXIT

echo "Starting Docker Compose services..."
docker compose up -d --wait --wait-timeout "${TIMEOUT}"

echo ""
echo "All services healthy."
echo ""
docker compose ps

echo ""
echo "Healthcheck verification complete."
