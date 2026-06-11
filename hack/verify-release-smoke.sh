#!/usr/bin/env bash
# verify-bucket: workspace
# verify-release-smoke runs the external-consumer smoke for the synchronized
# multi-module release (#1843, RELEASE-EXTERNAL-GET-01): it publishes the
# release-bumped library modules to a hermetic local file GOPROXY and proves an
# external consumer can `go get` + `go build` against them, with the full internal
# graph resolving to the published version.
#
# Bucketed `workspace` (a PR bucket, alongside verify-workspace.sh's module-graph
# checks): the test is fast and self-contained — it spawns nested `go get`/`go
# build` over an internal-deps-only file proxy with a fresh per-test GOMODCACHE,
# so the TEST EXECUTION is network-free and Docker-free. (The `go test` COMPILE
# step still needs the standard module cache for golang.org/x/mod + tools/gomodutil
# — present in CI's go.sum-keyed cache and on any workstation that ran the build.)
# NOTE: do NOT move this to the reserved `nightly` bucket — that bucket is excluded
# from the PR matrix and run only by archtest-nightly.yml's verify-archtest step,
# so a `nightly` gate here would never execute (governance.yml derives the matrix).

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

readonly PKG="./tools/releasesmoke"
readonly PKG_DIR="tools/releasesmoke"
readonly TIMEOUT="${TIMEOUT:-5m}"

# Build-tag self-check: the smoke is gated behind `//go:build releasesmoke`, and
# .github/workflows/_build-lint.yml lints tools under `--build-tags=…,releasesmoke`
# so the tagged source stays covered. If the tag were dropped from the source, that
# lint flag would silently cover nothing — fail closed here so the desync surfaces.
if ! grep -rlE '^//go:build releasesmoke' "$PKG_DIR" >/dev/null 2>&1; then
  echo "ERROR: no file under $PKG_DIR carries '//go:build releasesmoke' — the tag was" >&2
  echo "       dropped, so the _build-lint.yml '--build-tags=…,releasesmoke' flag is dead." >&2
  exit 1
fi

# Anti-vacuity: a moved/renamed file would silently run zero tests. Fail closed if
# discovery finds none.
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
