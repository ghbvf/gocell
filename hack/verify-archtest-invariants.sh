#!/usr/bin/env bash
# verify-archtest-invariants — PR-time archtest entry preserved by ADR
# 202605120000 §Amendment 2026-05-23 §D8. Original scope: 4 typed-AST
# invariants (clock-injection / duration-const / test-time-literal /
# panic-registered). Issue #19 added a 5th, lightweight, string-anchor
# invariant for `scripts/healthcheck-verify.sh` — it does no typed-AST
# load (it reads the script bytes and tokenizes), runs in <1s, and
# would otherwise wait for nightly to catch a regression.
#
# Why merged: each separate go test invocation cold-loads tools/archtest's
# typed AST (~5s/process via typeseval.SharedResolver). Running separate
# processes wastes ~5s/each on redundant packages.Load. Single process
# shares the SharedResolver cache key (root, false, nil, "./...") via
# testmain warmup (tools/archtest/testmain_test.go).
#
# Why no -race: archtest is read-only static analysis. SharedResolver
# concurrent-init race coverage is owned by test-race.yml::race-unit
# (./tools/archtest/internal/typeseval/...).

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

go test ./tools/archtest \
  -run '^(TestProdClockInjection|TestKernelClockLeafFallback|TestKernelClockLeafFallbackFixtures|TestProdClockInjectionFixtures|TestProdDurationConst|TestProdDurationConstFixtures|TestTestTimeLiteralConst|TestTestSleepDiscipline|TestTestTimeLiteralFixtures|TestPanicRegistered|TestPanicRegisteredScannerFixtures|TestHealthcheckVerifyScriptInvariant01|TestHealthcheckVerifyScriptInvariantMeta01)$' \
  -count=1 -timeout 5m
