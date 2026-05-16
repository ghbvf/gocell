#!/usr/bin/env bash
# verify-archtest runs the architectural unit-test suite (LAYER-*, AUTH-*,
# SEC-FAIL-CLOSED-*, ERROR-FIRST-API-01, META-QUERYPARAM-DRIFT, ADV-06, etc.)
# in process-isolated shards.
#
# Why sharded:
#   typeseval.SharedResolver caches *types.Info per cacheKey (modRoot, tests,
#   tags, patterns). Running all ~300 archtest top-level functions in one Go
#   process accumulates 20+ GB peak RSS on the GHA 2-core 7GB runner -> OOM
#   SIGTERM (observed after PR #445). Each shard is an independent `go test`
#   invocation; process exit releases the cached type graphs.
#
# Why no -race:
#   archtest is pure read-only static analysis (packages.Load + AST/types
#   walk + string compare). t.Parallel() subtests share no mutable state.
#   Race detector adds 2.5-3.3x runtime for zero signal on this surface.
#
# Env vars:
#   SHARD_COUNT       Number of shards to partition Test* functions across.
#                     Default 16 (Phase 0 measured max peak RSS 4.22 GB /
#                     shard on macOS for K=16; safe under Linux 7 GB GHA).
#   SHARD_TARGET      If set, run only that shard index [0, SHARD_COUNT).
#                     Used by CI matrix. If empty, all shards run serially
#                     in this process invocation.
#   TIMEOUT           Go test timeout per shard. Default 5m.
#   SLOWGATE_BIN      Path to slowgate binary. If executable, each shard's
#                     `go test -json` stream is piped through it. If unset/
#                     missing, slowgate is skipped (local dev).
#   DRY_RUN=1         Emit the discovered Test* names (one per line) and
#                     exit. Used by ARCHTEST-VERIFY-COVERAGE-01 to cross-
#                     check discovery against AST scan.
#   LIST_SHARD_TESTS=1
#                     With SHARD_TARGET set, emit only that shard's
#                     assigned Test* names (one per line) and exit. Used
#                     by ARCHTEST-VERIFY-COVERAGE-01 to validate the
#                     partition algorithm (exactly-once across shards).
#                     Goes through the same shard_pattern() function that
#                     run_shard() uses — single source of truth for the
#                     modulo assignment algorithm.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

readonly ARCHTEST_PKG="./tools/archtest"
readonly SHARD_COUNT="${SHARD_COUNT:-16}"
readonly TIMEOUT="${TIMEOUT:-5m}"
readonly SLOWGATE_BIN="${SLOWGATE_BIN:-}"
readonly SLOWGATE_THRESHOLD="${SLOWGATE_THRESHOLD:-20s}"
readonly SLOWGATE_ALLOWLIST="${SLOWGATE_ALLOWLIST:-tools/slowgate/allowlist.txt}"

# Validate SHARD_COUNT before doing any work.
if ! [[ "$SHARD_COUNT" =~ ^[1-9][0-9]*$ ]]; then
  echo "ERROR: SHARD_COUNT must be a positive integer, got '$SHARD_COUNT'" >&2
  exit 2
fi

# Discover all top-level Test* functions in archtest. Stable sort -> stable
# modulo partition across runs (so failure logs always point at the same
# shard).
TESTS=$(go test -list '^Test' "$ARCHTEST_PKG" | grep -E '^Test' | sort)
TOTAL=$(printf '%s\n' "$TESTS" | grep -c '^Test')

if [ "$TOTAL" -lt 1 ]; then
  echo "ERROR: no archtest Test* functions discovered in $ARCHTEST_PKG" >&2
  echo "       discovery command: go test -list '^Test' $ARCHTEST_PKG" >&2
  exit 1
fi

# Validate SHARD_TARGET if provided (relevant for SHARD_TARGET-aware modes
# below: LIST_SHARD_TESTS=1 and the actual shard execution path).
if [ -n "${SHARD_TARGET:-}" ]; then
  if ! [[ "$SHARD_TARGET" =~ ^[0-9]+$ ]] || [ "$SHARD_TARGET" -ge "$SHARD_COUNT" ]; then
    echo "ERROR: SHARD_TARGET must be in [0, $SHARD_COUNT), got '$SHARD_TARGET'" >&2
    exit 2
  fi
fi

# shard_assignment emits the modulo subset of $TESTS assigned to shard $1
# (one name per line). Single source of the partition algorithm — both
# run_shard (real test execution) and LIST_SHARD_TESTS (test introspection
# by ARCHTEST-VERIFY-COVERAGE-01) go through here. Changing the algorithm
# requires editing this function only; the partition exactly-once
# conformance test catches breaks in either direction.
shard_assignment() {
  local shard=$1
  printf '%s\n' "$TESTS" \
    | awk -v s="$shard" -v n="$SHARD_COUNT" 'NR % n == s'
}

# DRY_RUN=1: emit discovered Test* names (one per line) and exit. Used by
# ARCHTEST-VERIFY-COVERAGE-01 for cross-check against AST scan.
if [ "${DRY_RUN:-}" = "1" ]; then
  printf '%s\n' "$TESTS"
  exit 0
fi

# LIST_SHARD_TESTS=1 (requires SHARD_TARGET): emit the assignment for that
# shard and exit. Used by ARCHTEST-VERIFY-COVERAGE-01 to verify
# exactly-once partition across all shards.
if [ "${LIST_SHARD_TESTS:-}" = "1" ]; then
  if [ -z "${SHARD_TARGET:-}" ]; then
    echo "ERROR: LIST_SHARD_TESTS=1 requires SHARD_TARGET to be set" >&2
    exit 2
  fi
  shard_assignment "$SHARD_TARGET"
  exit 0
fi

echo "verify-archtest: discovered $TOTAL Test* functions, SHARD_COUNT=$SHARD_COUNT"

run_shard() {
  local shard=$1
  local pattern
  pattern=$(shard_assignment "$shard" | tr '\n' '|' | sed 's/|$//')
  if [ -z "$pattern" ]; then
    echo "[shard $shard/$SHARD_COUNT] no tests assigned (TOTAL=$TOTAL)"
    return 0
  fi
  local count
  count=$(printf '%s\n' "$pattern" | tr '|' '\n' | grep -c '^Test')
  echo "=== shard $shard/$SHARD_COUNT ($count tests) ==="

  if [ -n "$SLOWGATE_BIN" ] && [ -x "$SLOWGATE_BIN" ]; then
    # CI path: tee the -json stream to a per-shard file before piping
    # through slowgate, mirroring _build-lint.yml's build-test pattern.
    # Without tee, slowgate consumes the event stream and only its own
    # threshold-summary survives in stderr — insufficient for arbitrary
    # test panics / build errors / sub-test stack traces. The CI job's
    # `if: failure()` artifact upload step exposes these files on failure.
    # set -o pipefail catches a go test failure even when slowgate exits 0.
    local artifact_dir="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
    go test -count=1 -timeout "$TIMEOUT" -json -run "^($pattern)$" "$ARCHTEST_PKG" \
      | tee "${artifact_dir}/archtest-shard-${shard}.json" \
      | "$SLOWGATE_BIN" --threshold="$SLOWGATE_THRESHOLD" --allowlist="$SLOWGATE_ALLOWLIST"
  else
    # Local path: no slowgate binary, run plain.
    go test -count=1 -timeout "$TIMEOUT" -run "^($pattern)$" "$ARCHTEST_PKG"
  fi
}

if [ -n "${SHARD_TARGET:-}" ]; then
  run_shard "$SHARD_TARGET"
else
  for s in $(seq 0 $((SHARD_COUNT - 1))); do
    run_shard "$s"
  done
fi

echo "verify-archtest: PASS (SHARD_COUNT=$SHARD_COUNT, TOTAL=$TOTAL)"
