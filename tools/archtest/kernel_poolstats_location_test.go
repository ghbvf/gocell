//go:build archtest

// INVARIANT: KERNEL-POOLSTATS-LOCATION-01a
// INVARIANT: KERNEL-POOLSTATS-LOCATION-01b
//
// # KERNEL-POOLSTATS-LOCATION-01
//
// Invariants:
//
//	01a — `runtime/observability/poolstats` is forbidden as an import path.
//	      The pool-stats Statter is a contract-only package (zero imports,
//	      pure Snapshot + Statter interface) consumed by adapters and
//	      produced by adapters; it lives at `kernel/observability/poolstats`.
//	      Once descended (M0-FOUNDATION), the old runtime path must never
//	      come back — including via type alias or re-export shim.
//
//	01b — `kernel/observability/poolstats` must be import-zero (stdlib only).
//	      The package is a contract: a Snapshot value type and a Statter
//	      interface. Bringing in errcode / pkg / yaml.v3 would couple the
//	      contract to runtime concerns and re-create the very layer
//	      entanglement that motivated the descent.
//
// Refs: M0-FOUNDATION (kernel poolstats isolation)
//
// Note: poolstatsForbiddenImport, poolstatsCanonicalDir, scanPoolstatsNonStdlibImports,
// and isPoolstatsStdlibImport are defined in kernel_poolstats_location.go
// (non-test file) anchored to PlatformModulePath.
package archtest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKERNEL_POOLSTATS_LOCATION_01a_NoLegacyImport walks every .go file in
// the module (production + tests) and fails when any file imports the legacy
// `runtime/observability/poolstats` path.
func TestKERNEL_POOLSTATS_LOCATION_01a_NoLegacyImport(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	ImportBan{
		RuleID:    "KERNEL-POOLSTATS-LOCATION-01a",
		Forbidden: []string{poolstatsForbiddenImport},
		AllowRels: []string{"tools/archtest/kernel_poolstats_location_test.go"},
		Hint:      `descend to "` + PlatformModulePath + "/" + poolstatsCanonicalDir + `"`,
	}.Run(t, ModuleScope(root, IncludeTests()))
}

// TestKERNEL_POOLSTATS_LOCATION_01b_ContractIsImportZero scans every
// production .go file under kernel/observability/poolstats/ and fails when
// any non-stdlib import is found. Stdlib paths are identified by the absence
// of a `.` in their first segment (Go community convention: stdlib packages
// have no domain).
//
// Pre-descent: if kernel/observability/poolstats does not exist yet, DirsScope
// returns an empty file set and this test passes vacuously (no files to scan,
// no violations possible). This is correct — the constraint is vacuously true
// before the directory exists; 01a keeps the old path forbidden in the meantime.
func TestKERNEL_POOLSTATS_LOCATION_01b_ContractIsImportZero(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	scope := DirsScope(root, []string{poolstatsCanonicalDir})
	diags := Run(t, AST(scope), scanPoolstatsNonStdlibImports)

	Report(t, "KERNEL-POOLSTATS-LOCATION-01b", diags)
}

// INVARIANT: KERNEL-POOLSTATS-LOCATION-01b
//
// TestKERNEL_POOLSTATS_LOCATION_01b_ScannerDetectsViolation loads the
// fixture package tools/archtest/internal/poolstatsfixture/violation and
// asserts that scanPoolstatsNonStdlibImports detects the non-stdlib import
// as a violation.
//
// Per ai-robust.md §"real source AST capture": the fixture is a real Go
// package loaded via packages.Load through the Fixture driver. Bypassing
// this test requires modifying real source — a hand-crafted AST is
// insufficient because the scanner inspects the actual import spec path.
func TestKERNEL_POOLSTATS_LOCATION_01b_ScannerDetectsViolation(t *testing.T) {
	t.Parallel()

	diags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/poolstatsfixture/violation"}),
		scanPoolstatsNonStdlibImports)

	require.NotEmpty(t, diags,
		"scanner must detect non-stdlib import in poolstatsfixture/violation")
}
