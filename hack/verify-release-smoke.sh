#!/usr/bin/env bash
# verify-bucket: nightly
# verify-release-smoke runs the external-consumer smoke for the synchronized
# multi-module release (#1843, RELEASE-EXTERNAL-GET-01): it publishes the
# release-bumped library modules to a hermetic local file GOPROXY and proves an
# external consumer can `go get` + `go build` against them, with the full internal
# graph resolving to the published version.
#
# Bucketed nightly (not a PR bucket): the test spawns nested `go get`/`go build`
# subprocesses, mirroring verify-archtest.sh's placement, to keep the PR hot path
# fast. It is network-free (internal-deps-only proxy projections + a fresh
# per-test GOMODCACHE) and Docker-free.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

readonly PKG="./tools/releasesmoke"
readonly TIMEOUT="${TIMEOUT:-5m}"

# Anti-vacuity: the smoke is build-tagged `releasesmoke`; a dropped tag or moved
# file would silently run zero tests. Fail closed if discovery finds none.
TESTS=$(go test -tags=releasesmoke -list '^Test' "$PKG" 2>/dev/null | grep -E '^Test' || true)
TOTAL=$(printf '%s\n' "$TESTS" | grep -c '^Test' || true)
if [ "${TOTAL:-0}" -lt 1 ]; then
  echo "ERROR: no releasesmoke Test* functions discovered in $PKG (build-tag drift?)" >&2
  echo "       discovery: go test -tags=releasesmoke -list '^Test' $PKG" >&2
  exit 1
fi

echo "verify-release-smoke: running $TOTAL test(s) in $PKG"
go test -tags=releasesmoke -count=1 -timeout "$TIMEOUT" "$PKG"
echo "verify-release-smoke: PASS ($TOTAL tests)"
