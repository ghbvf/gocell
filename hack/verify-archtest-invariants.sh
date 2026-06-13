#!/usr/bin/env bash
# verify-bucket: inv
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

# shellcheck source=lib/archtest.sh
source hack/lib/archtest.sh

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
# this PR-time set even though governance.yml excludes the full archtest sweep
# from its PR buckets (verify-archtest.sh -> `# verify-bucket: nightly`, #1817):
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
#
# ARCHTEST-LEAF-BUILD-TAG-01 (TestArchtest_AllLeafTestFiles_HaveArchtestBuildTag)
# IS in this PR-time set: it is the upstream completeness guard of the build-tag
# funnel that keeps the heavy archtest suite OFF the make verify critical path. A
# new leaf *_test.go that forgets `//go:build archtest` re-leaks the suite into
# the bare `go test ./...` traversal (verify-workspace-test, build-test tools
# shard) — a SILENT (green) perf regression that must fail at PR-merge, not only
# nightly. Cheap here: a header-only //go:build parse per file, NO packages.Load.
#
# PERMISSION-BASED-AUTHZ-01 — its migration allowlist was DRAINED to empty by
# PR-10c (#1348). Two CHEAP guards keep the rule at PR-time so it does not silently
# regress to nightly-only — the failure PR #1974 review F3 caught when the former
# TestPermissionBasedAuthzAllowlist_Ceiling was deleted with no PR-time replacement:
#   TestPermissionBasedAuthzAllowlist_FrozenEmpty — in-memory exact-set freeze at ∅
#     (NO packages.Load): a re-grow that re-opens a role-literal-gate exemption fails
#     at PR-merge, not just nightly. Replaces the deleted Ceiling, frozen at empty.
#   TestPermissionBasedAuthz_ReverseFixture — loads the standalone RED fixture
#     (~1.7s, ONE tiny module, NOT the corecells tree) and asserts the scan fires,
#     proving the typed scanner is non-vacuous at PR-merge.
# The heavy whole-corecells packages.Load scan (TestPermissionBasedAuthz_01), like
# CLOCK-POSITIONAL-INJECTION-01 above, stays nightly (archtest-nightly.yml) to respect
# the 2-CPU/7GB runner budget — a real new gate in a live corecells handler is caught
# there. #1925 PR-10b pr-review F2/F3; #1974 PR-10c pr-review F3.
#
# -tags=archtest: the archtest leaf is gated behind `//go:build archtest` so a
# bare `go test ./...` keeps it off the make verify / PR critical path (build-tag
# funnel; same convention as integration / e2e). This gate is a sanctioned
# PR-time OWNER and opts in. The non-vacuity check below converts a missing /
# renamed tag (which yields "[no test files]" → 0 tests → false green) into a
# hard failure — `-run` over an empty test set exits 0 otherwise.
if ! output="$(go test -tags="$ARCHTEST_BUILD_TAGS" ./tools/archtest \
  -run '^(TestProdClockInjection|TestKernelClockLeafFallback|TestKernelClockLeafFallbackFixtures|TestProdClockInjectionFixtures|TestProdDurationConst|TestProdDurationConstFixtures|TestTestTimeLiteralConst|TestTestSleepDiscipline|TestTestTimeLiteralFixtures|TestPanicRegistered|TestPanicRegisteredScannerFixtures|TestArchtestModulePathFunnel|TestModulePathFunnel_TypedReconstruction|TestFenceTokenMintFunnel_AllowlistEnforced|TestMetadatatestImportScope|TestFixtureCellIDTypedBuilder_NewCellIDBodyShape|TestFixtureCellIDTypedBuilder_VarInitializerShape|TestRootModuleNoReplace01|TestRootModuleNoReplace01_NegativeControl|TestPlatformCellScanCoverage01|TestPlatformCellScanCoverage01_AntiVacuity|TestArchtest_AllLeafTestFiles_HaveArchtestBuildTag|TestArchtest_InvariantsScriptContainsFunnelGuard|TestPermissionBasedAuthzAllowlist_FrozenEmpty|TestPermissionBasedAuthz_ReverseFixture)$' \
  -count=1 -timeout 5m 2>&1)"; then
  printf '%s\n' "$output"
  exit 1
fi
printf '%s\n' "$output"
if printf '%s' "$output" | grep -qE 'no test files|no tests to run'; then
  echo "verify-archtest-invariants: ZERO archtest tests ran — missing -tags=archtest? (build-tag funnel, see hack/verify-archtest.sh)" >&2
  exit 1
fi
