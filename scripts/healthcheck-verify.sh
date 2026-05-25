#!/usr/bin/env bash
# healthcheck-verify.sh
# Boots all Docker Compose services, waits up to TIMEOUT seconds for every
# service to pass its built-in health check, prints status, then tears down.
#
# INVARIANT (HEALTHCHECK-VERIFY-CLEANUP-AND-WAIT-TIMEOUT-01, issue #19):
#   - `trap ... EXIT` must be installed before the first `docker compose`
#     call so cleanup runs on success, `set -e` early-exit, and SIGINT.
#   - the trap body must invoke `docker compose down` (not a noop) — the
#     archtest cross-checks the body to rule out an empty cleanup.
#   - cleanup must propagate its own failure: if the main flow succeeded
#     but `docker compose down` failed, the script exits non-zero so the
#     caller doesn't see "success + orphan containers". When the main
#     flow already failed, the original failure code is preserved.
#   - `docker compose up --wait` must be bounded by `--wait-timeout`,
#     never bare `--timeout` (that's the stop-shutdown flag and is
#     silently ignored as a wait bound).
# All four are enforced by tools/archtest/healthcheck_verify_script_test.go.
#
# Exit 0 on success, non-zero if any service fails within the timeout
# OR if cleanup itself fails on an otherwise-clean run.

set -euo pipefail

TIMEOUT=30

_cleanup() {
  local _rc=$?
  echo "[healthcheck-verify] tearing down containers..." >&2
  # Note: do NOT use `if ! docker compose down; then ... fi` here. Inside
  # the then-branch of `if ! cmd`, $? is the result of the `!` expression
  # (always 0), not of cmd. The if-then-else form below preserves cmd's
  # real exit code in the else-branch's $?. Pinned by archtest's runtime
  # arm (TestHealthcheckVerifyRuntime01_CleanupFailurePropagates).
  if docker compose down; then
    :
  else
    local _down_rc=$?
    echo "[healthcheck-verify] WARNING: docker compose down exited $_down_rc — orphan containers may remain" >&2
    # If the main flow succeeded, surface the cleanup failure instead of
    # masking it with exit 0 (orphan containers + exit 0 misleads CI).
    # If the main flow already failed, keep the original failure code.
    if [ "$_rc" -eq 0 ]; then
      _rc=$_down_rc
    fi
  fi
  exit "$_rc"
}

trap _cleanup EXIT

echo "Starting Docker Compose services..."
docker compose up -d --wait --wait-timeout "${TIMEOUT}"

echo ""
echo "All services healthy."
echo ""
docker compose ps

echo ""
echo "Healthcheck verification complete."
