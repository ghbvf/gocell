//go:build archtest_fixture

// Package passfunnelfixture contains intentionally-violating archtest entry
// point usages that exercise the three PASS-FUNNEL-* meta-archtest detectors
// in pass_funnel_test.go. Gated by the archtest_fixture build tag (kept as
// a literal here because Go's //go:build syntax cannot reference Go
// constants — must agree with [archtestmeta.FixtureBuildTag]).
//
// TestPassFunnel_FixtureCoverage loads this package with the
// archtest_fixture tag via typeseval.SharedResolver and asserts each rule
// detector emits ≥ 1 diagnostic. Removing or modifying any of the
// reference lines below turns one of the coverage assertions red — locking
// the rule pipeline at the live-AST level rather than the data-snapshot
// level (per AI-rebust charter "盲区自检").
//
// The fixture uses VALUE references (`_ = scanner.EachFile`) instead of
// call expressions. The PASS-FUNNEL detectors run typeseval.ResolvePackageRef
// over every SelectorExpr / bare Ident in the file, and ResolvePackageRef
// does NOT distinguish a function value reference from a call site (both
// produce *types.Func or *types.PkgName resolutions). Using value
// references lets the fixture stay free of testing import, *testing.T
// parameters, or scope.ModuleScope("") boilerplate that would obscure the
// detector contract.
//
// # Forms covered
//
// Each banned symbol is referenced in three import shapes so the detector
// is exercised across the AST forms typeseval.ResolvePackageRef resolves:
//
//   - qualified-import (`scannerpkg.EachFile` after named/regular import)
//   - alias-import     (`sn.EachFile` after `import sn "<path>"`)
//   - dot-import       (`EachFile` after `import . "<path>"` — bare Ident
//     scan; sister rule SCANNER-FRAMEWORK-USAGE-01's Path A.1+A.3 shape)
//
// Plus a direct packages-import violation for PASS-FUNNEL-PACKAGES-IMPORT-01.
package passfunnelfixture

import (
	// VIOLATION: PASS-FUNNEL-PACKAGES-IMPORT-01 (qualified import path scan).
	"golang.org/x/tools/go/packages"

	// VIOLATION sources for PASS-FUNNEL-EACHFILE-01 — qualified + alias + dot forms.
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	sn "github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	. "github.com/ghbvf/gocell/tools/archtest/internal/scanner"

	// VIOLATION sources for PASS-FUNNEL-LOADPACKAGES-01 — qualified + alias + dot forms.
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	te "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// VIOLATION samples — value references suffice for typeseval.ResolvePackageRef
// to resolve the package+symbol pair (no call expression needed). Listed at
// file scope so the AST contains stable SelectorExpr / Ident nodes for the
// detector to walk. None of these are ever invoked at runtime; the file
// must merely type-check so packages.Load delivers full TypesInfo to the
// archtest pipeline.
var (
	// PASS-FUNNEL-EACHFILE-01 violations
	_ = scanner.EachFile // qualified
	_ = sn.EachFile      // alias-import
	_ = EachFile         // dot-import (bare Ident)

	// PASS-FUNNEL-LOADPACKAGES-01 violations
	_ = typeseval.LoadPackages   // qualified
	_ = typeseval.SharedResolver // qualified
	_ = te.LoadPackages          // alias-import
	_ = te.SharedResolver        // alias-import

	// Force a packages reference so the import is not elided. The Config
	// type usage exists only to keep the import live; the import itself is
	// the PASS-FUNNEL-PACKAGES-IMPORT-01 violation (path scan, not symbol).
	_ packages.Config
)
