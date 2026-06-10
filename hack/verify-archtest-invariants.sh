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

# nightly-only (whole-module typed scan, not eligible for PR-time due to
# packages.Load memory/time budget on 2-CPU CI runners):
#   IMPL-DECL-COVER-01   (TestImplDeclCover)
#   HANDLER-DECL-COVER-01 (TestHandlerDeclCover)
#   EMIT-DECL-COVER-01   (TestEmitDeclCover)
#   DEAD-CONTRACT-01     (TestDeadContractCover)
#   DEAD-CODE-01         (TestDeadCodeCover)
# These run in archtest-nightly.yml (full 24-shard suite) and locally via
# `make verify` / `bash hack/verify-archtest.sh`.

# CLOCK-POSITIONAL-INJECTION-01 (TestClockPositionalInjection + …Fixtures) is
# intentionally NOT in this PR-time set: it is a whole-prod-tree packages.Load
# scan whose memory footprint, added to the ~5 existing whole-tree scans here,
# exceeds the 2-CPU/7GB CI runner budget (SIGTERM/OOM at ~84s). It stays gated
# nightly via archtest-nightly.yml (full 24-shard suite) + local `make verify`
# (verify-archtest.sh full sweep). #1053 review F6 (add to PR-time) reverted for
# CI stability — the heavy whole-module form-lock belongs with the nightly suite.
#
# ROOT-MODULE-NO-REPLACE-01 (TestRootModuleNoReplace01 + …NegativeControl) IS in
# this PR-time set even though governance.yml VERIFY_SKIPs the full archtest sweep:
# it guards a user-visible release-surface promise (README / external-cell
# quickstart: "public module, plain `go get` — no GOPRIVATE"), so a regression — a
# replace/exclude added back to the ROOT go.mod — must fail at PR-merge time, not
# only nightly. It is cheap here: a go.mod parse via gomodutil, NO whole-tree
# packages.Load, so it adds ~0 to the shared-resolver process (warm-up already paid
# by the tests above). #1723 / PR #1766 review F1.
#
# PLATFORM-CELL-SCAN-COVERAGE-01 (TestPlatformCellScanCoverage01 + …_AntiVacuity)
# IS in this PR-time set: it is the keystone anti-vacuity guard for the #1560
# go.work P5 split — a relayout that drops corecells from the workspace, empties
# its scan root, or lets a platform-cell production file escape the scan set must
# fail at PR-merge time, not only nightly, or every downstream DirsScope cell rule
# silently fail-opens for a full day. Cheap here: go.work parse + metadata
# discovery, NO whole-tree packages.Load. #1560 / PR #1814 review F7.
go test ./tools/archtest \
  -run '^(TestProdClockInjection|TestKernelClockLeafFallback|TestKernelClockLeafFallbackFixtures|TestProdClockInjectionFixtures|TestProdDurationConst|TestProdDurationConstFixtures|TestTestTimeLiteralConst|TestTestSleepDiscipline|TestTestTimeLiteralFixtures|TestPanicRegistered|TestPanicRegisteredScannerFixtures|TestArchtestModulePathFunnel|TestModulePathFunnel_TypedReconstruction|TestFenceTokenMintFunnel_AllowlistEnforced|TestMetadatatestImportScope|TestFixtureCellIDTypedBuilder_NewCellIDBodyShape|TestFixtureCellIDTypedBuilder_VarInitializerShape|TestRootModuleNoReplace01|TestRootModuleNoReplace01_NegativeControl|TestPlatformCellScanCoverage01|TestPlatformCellScanCoverage01_AntiVacuity)$' \
  -count=1 -timeout 5m
