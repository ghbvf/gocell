#!/usr/bin/env bash
# G6 TEST-TIME-LITERAL-01 gate.
# Invariant + scope: see docs/plans/202605011500-029-master-roadmap.md G6.
# Implementation: tools/archtest/test_time_literal_test.go

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go test ./tools/archtest/ -run TestTestTimeLiteralConst -count=1 -timeout 5m -v
