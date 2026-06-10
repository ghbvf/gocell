#!/usr/bin/env bash
# Verifies that agent instruction rules stay short and future-facing.
# `make verify` discovers this gate automatically.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=lib/archtest.sh
source hack/lib/archtest.sh

# -tags=archtest: archtest leaf is build-tagged (//go:build archtest) to stay off
# the bare `go test ./...` critical path (build-tag funnel). This gate is a
# sanctioned OWNER and opts in; the non-vacuity check turns a missing/renamed tag
# ("[no test files]" → 0 tests → false green) into a hard failure.
if ! output="$(go test -tags="$ARCHTEST_BUILD_TAGS" ./tools/archtest -run '^TestAgentRulesGovernance$' -count=1 -timeout 30s 2>&1)"; then
  printf '%s\n' "$output"
  exit 1
fi
printf '%s\n' "$output"
if printf '%s' "$output" | grep -qE 'no test files|no tests to run'; then
  echo "verify-rules-governance: ZERO archtest tests ran — missing -tags=archtest? (build-tag funnel, see hack/verify-archtest.sh)" >&2
  exit 1
fi
