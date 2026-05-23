#!/usr/bin/env bash
# verify-archtest-invariants — single entry for the 4 PR-time archtest invariants
# preserved by ADR 202605120000 §Amendment 2026-05-23 决策第 5 项.
#
# Why merged: each separate go test invocation cold-loads tools/archtest's
# typed AST (~5s/process via typeseval.SharedResolver). Running 4 separate
# processes wastes ~15s on redundant packages.Load. Single process shares
# the SharedResolver cache key (root, false, nil, "./...") via testmain
# warmup (tools/archtest/testmain_test.go).
#
# Why no -race: archtest is read-only static analysis. SharedResolver
# concurrent-init race coverage is owned by test-race.yml::race-unit
# (./tools/archtest/internal/typeseval/...).

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

go test ./tools/archtest \
  -run '^(TestProdClockInjection|TestKernelClockLeafFallback|TestProdClockInjectionFixtures|TestProdDurationConst|TestProdDurationConstFixtures|TestTestTimeLiteralConst|TestTestSleepDiscipline|TestTestTimeLiteralFixtures|TestPanicRegistered|TestPanicRegisteredScannerFixtures)$' \
  -count=1 -timeout 5m
