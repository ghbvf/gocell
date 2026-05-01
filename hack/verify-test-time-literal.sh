#!/usr/bin/env bash
# G6 TEST-TIME-LITERAL-01 + TEST-SLEEP-DISCIPLINE-01 gates.
#
# Canonical owner: this script is the single source of truth for these gates
# in Governance Strict (governance.yml -> make verify auto-discovery). The
# `tools` shard's `go test ./tools/archtest/...` runs the same tests as
# transitive coverage; do not migrate ownership without updating both
# governance.yml and .github/workflows/_build-lint.yml.
#
# -race is enabled to mirror verify-prod-duration.sh; the loader uses a
# shared sync.Mutex + singleflight that we want race-checked under load.
#
# Invariant + scope: see docs/plans/202605011500-029-master-roadmap.md G6.
# Implementations:
#   tools/archtest/test_time_literal_test.go
#   tools/archtest/test_sleep_discipline_test.go

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go test ./tools/archtest/ \
  -run 'TestTestTimeLiteralConst|TestTestSleepDiscipline|TestTestTimeLiteralFixtures' \
  -race -count=1 -timeout 5m -v
