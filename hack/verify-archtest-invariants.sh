#!/usr/bin/env bash
# verify-archtest-invariants — single entry for the 4 PR-time archtest invariants
# preserved by ADR 202605120000 §Amendment 2026-05-23 §D8 (4 类 PR-time archtest gate).
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

# CLOCK-POSITIONAL-INJECTION-01 (TestClockPositionalInjection + …Fixtures) is
# intentionally NOT in this PR-time set: it is a whole-prod-tree packages.Load
# scan whose memory footprint, added to the ~5 existing whole-tree scans here,
# exceeds the 2-CPU/7GB CI runner budget (SIGTERM/OOM at ~84s). It stays gated
# nightly via archtest-nightly.yml (full 16-shard suite) + local `make verify`
# (verify-archtest.sh full sweep). #1053 review F6 (add to PR-time) reverted for
# CI stability — the heavy whole-module form-lock belongs with the nightly suite.
go test ./tools/archtest \
  -run '^(TestProdClockInjection|TestKernelClockLeafFallback|TestKernelClockLeafFallbackFixtures|TestProdClockInjectionFixtures|TestProdDurationConst|TestProdDurationConstFixtures|TestTestTimeLiteralConst|TestTestSleepDiscipline|TestTestTimeLiteralFixtures|TestPanicRegistered|TestPanicRegisteredScannerFixtures|TestFenceTokenMintFunnel_AllowlistEnforced)$' \
  -count=1 -timeout 5m
