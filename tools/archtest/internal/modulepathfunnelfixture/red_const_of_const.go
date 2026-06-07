//go:build archtest_fixture

// Package modulepathfunnelfixture is the RED/GREEN reverse self-check fixture
// for ARCHTEST-MODULE-PATH-FUNNEL-01's typed const-eval reconstruction detector
// (#1304 sub-item ③ downstream residual closure).
//
// Each file is one isolated case; the self-check test
// (TestModulePathFunnel_TypedReconstruction) loads this package via the typed
// Fixture scope, runs collectPlatformReconstructions, and asserts which FILES
// the detector flags:
//
//   - red_const_of_const.go — residual (a): const-of-const fragment chain. MUST flag.
//   - red_cross_pkg.go      — residual (b): cross-package const fragment.   MUST flag.
//   - green_sanctioned.go   — sanctioned PlatformModulePath derivation.     MUST NOT flag.
//   - green_unrelated.go    — non-platform concat.                          MUST NOT flag.
//   - green_runtime_ops.go  — residual (c): runtime string ops (permanent ceiling). MUST NOT flag.
//
// It lives under internal/ so it is excluded from the production funnel scan by
// inFunnelScope (which excludes tools/archtest/internal/), and from the main
// reconstruction scan (which loads only the top-level ./tools/archtest package).
package modulepathfunnelfixture

// const-of-const: aliasGocell aliases baseGocell via an Ident RHS, so the old
// AST collectStringLiteralConsts (which collected only single-string-LITERAL-RHS
// consts) did NOT resolve aliasGocell — leaving "github.com/ghbvf/" + aliasGocell
// unresolvable and the reconstruction invisible. go/types constant folding DOES
// resolve it, so the typed detector catches it. This is residual (a).
const (
	baseGocell  = "gocell"
	aliasGocell = baseGocell
)

// The blank var folds to the bare platform module path (github.com/ghbvf/gocell)
// reconstructed from a literal fragment plus a const-of-const. The typed detector
// MUST flag this BinaryExpr. (Blank identifier so the fixture stays unused-clean,
// matching the func _() convention of the other fixtures.)
var _ = "github.com/ghbvf/" + aliasGocell
