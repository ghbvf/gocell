// Package archtestmeta carries migration-period metadata for the archtest
// pass-funnel migration (refactor/574-archtest-pass-funnel-pr1 onwards).
//
// THIS PACKAGE IS A FIXED-TERM MIGRATION SCAFFOLD.
//
// Stage 4 of the migration (see docs/plans/202605141519-040-archtest-pass-funnel-plan.md
// and ADR docs/architecture/202605141519-adr-archtest-pass-funnel.md) deletes
// this package in its entirety. The permanent enforcement layers are:
//
//   - Type system: archtest.Pass.Pkg is *types.Package (no .Syntax access);
//   - Lint: depguard rule archtest-no-direct-packages-load in .golangci.yml;
//   - Meta-archtest: PASS-FUNNEL-EACHFILE-01 / LOADPACKAGES-01 / PACKAGES-IMPORT-01.
//
// While the migration is in progress, the meta-archtest consults
// [LegacyAllowlist] to skip files that have not yet been ported from the
// scanner / typeseval direct entry points to archtest.Pass.
//
// .golangci.yml's archtest-no-direct-packages-load rule carries a separate
// (smaller) negative-glob list: only files that DIRECTLY import
// golang.org/x/tools/go/packages need a depguard exemption, which is a
// subset of this LegacyAllowlist. Stage 2/3 PRs porting a file MUST
// remove its entry here AND — if the file imports packages directly —
// the matching negative-glob in .golangci.yml. The
// TestPassFunnelGuardListSync archtest cross-validates the two lists.
package archtestmeta

// LegacyAllowlist enumerates archtest *_test.go files (module-relative slash
// paths) that still use scanner.EachFile, typeseval.LoadPackages,
// typeseval.SharedResolver, or directly import golang.org/x/tools/go/packages
// as of refactor/574-archtest-pass-funnel-pr1 (stage 1 baseline).
//
// Mutation rules:
//
//   - Stage 2/3 PRs remove exactly one entry as they port that file to
//     archtest.Pass + Run/RunTyped. If the file also directly imports
//     golang.org/x/tools/go/packages, its negative-glob entry in
//     .golangci.yml's archtest-no-direct-packages-load rule must be
//     removed in the same commit. TestPassFunnelGuardListSync fail-loud
//     enforces this — manual drift becomes a CI failure, not a silent
//     reviewer-only check.
//   - Stage 4 PR empties the map AND deletes this package entirely.
//
// FixtureBuildTag is the build-tag string shared by every archtest fixture
// sub-package whose violation samples must remain invisible to the default
// build context. Used by:
//
//   - tools/archtest/internal/passfunnelfixture/redfixture.go (`//go:build
//     archtest_fixture` directive — kept as a literal, since Go's build
//     directive syntax cannot reference Go constants).
//   - tools/archtest/pass_funnel_test.go: TestPassFunnel_FixtureCoverage
//     loads the fixture package with this tag passed to
//     typeseval.SharedResolver.
//
// The two sites must agree. The build directive in redfixture.go carries a
// godoc cross-reference to this constant; changing the value here without
// updating the directive (or vice versa) leaves the fixture invisible to
// the coverage test, which fails fast on "loaded with 0 files".
const FixtureBuildTag = "archtest_fixture"

// LegacyAllowlist is now empty — all migration-period entries have been
// ported to archtest.Pass (Stage 2/3 complete). Stage 4 will delete this
// package entirely; the empty map is retained until then so the
// TestPassFunnelGuardListSync cross-validator continues to compile.
//
// Lookup is by module-relative slash path (e.g. "tools/archtest/foo_test.go").
var LegacyAllowlist = map[string]bool{}
