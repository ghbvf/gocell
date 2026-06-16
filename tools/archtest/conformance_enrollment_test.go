//go:build archtest

// INVARIANT: CONFORMANCE-ENROLLMENT-SINGLE-LOAD-01
//
// AI-robust: Medium (type-unaware AST self-scan over a single known file).
//
// The conformance-enrollment rule family (#2249) folds the historical
// Tests=false-iface + Tests=true-scan two-load design into ONE Tests=true
// packages.Load issued by loadConformanceEnrollmentImpls. This guard pins that
// single-load property: conformance_enrollment.go must contain EXACTLY ONE
// archtest.Run dispatch (the shared core's load). Reintroducing a second load —
// the regression this folds away — makes the count ≥2 and turns this test RED in
// CI, before the heavier nightly load-cost regression resurfaces.
//
// Hard is not achievable: Go cannot compile-forbid a second Run call (a genuine
// language ceiling); this is the Medium machine-checkable backstop. The load
// FUNNEL itself stays Hard via PASS-FUNNEL-LOADPACKAGES-01 (façade-only loading).
//
// Why a self-scan (not a packages.Load archtest): keeping this guard parse-only
// (one file, no go/types) keeps it off the nightly slowgate's heavy-test budget —
// the very cost class #2249 reduces.
//
// Synthetic RED case: a real RED fixture is impossible here (the scanned target IS
// the rule's own source file, not a separate fixture module). The equivalent is the
// "synthetic_red_green" sub-test, which feeds the SAME counter (countCalleeIdent) a
// crafted source with two Run calls and asserts it returns 2 — proving the detector
// would flag a reintroduced second load, not vacuously pass. Anti-vacuity (coreFn
// must exist) covers the wrong-file/rename failure mode.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// INVARIANT: CONFORMANCE-ENROLLMENT-SINGLE-LOAD-01

// TestConformanceEnrollmentSingleLoad01 asserts the folded conformance-enrollment
// core issues exactly one packages.Load (one archtest.Run dispatch).
func TestConformanceEnrollmentSingleLoad01(t *testing.T) {
	t.Parallel()

	const (
		file   = "conformance_enrollment.go"
		coreFn = "loadConformanceEnrollmentImpls"
	)

	// NOTE: `file` is a bare relative path; `go test` runs with cwd = the package
	// dir (tools/archtest), so it resolves. require.NoError fails loud if a future
	// runner changes cwd.
	f := parseSelfSource(t, file)

	// Anti-vacuity: the core function must exist (else the guard scans the wrong
	// file and silently passes).
	require.Equal(t, 1, countFuncDecls(f, coreFn),
		"expected exactly one %s declaration in %s (anti-vacuity); the guard is scanning the wrong file or the core was renamed",
		coreFn, file)

	// The single-load invariant: exactly one Run dispatch across the whole file.
	// loadConformanceEnrollmentImpls holds it; the repo/saga family check bodies
	// delegate to that core and issue no direct load.
	runCalls := countRunDispatches(f)
	assert.Equal(t, 1, runCalls,
		"CONFORMANCE-ENROLLMENT-SINGLE-LOAD-01: %s must contain exactly ONE archtest.Run "+
			"(the folded single Tests=true load, #2249); got %d. A second load reintroduces the "+
			"halved-away nightly load cost — keep iface resolution + impl collection + test scan in one load.",
		file, runCalls)

	// Synthetic RED/GREEN: prove the counter is non-vacuous — it must report 2 for a
	// two-Run source (the regression this guard catches) and 1 for a one-Run source.
	t.Run("synthetic_red_green", func(t *testing.T) {
		t.Parallel()
		redSrc := "package p\nfunc x(){ Run(a); Run(b) }\n"
		greenSrc := "package p\nfunc x(){ Run(a) }\n"
		assert.Equal(t, 2, countRunDispatches(parseSrc(t, redSrc)),
			"synthetic RED: counter must report 2 Run calls (so a reintroduced second load turns the guard RED)")
		assert.Equal(t, 1, countRunDispatches(parseSrc(t, greenSrc)),
			"synthetic GREEN: counter must report 1 Run call")
	})
}

// parseSelfSource parses a sibling source file in this package (cwd = package dir
// under `go test`); fails loud on a missing/unreadable file.
func parseSelfSource(t *testing.T, name string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
	require.NoError(t, err, "parse %s", name)
	return f
}

// parseSrc parses an in-memory Go source string (for synthetic detector cases).
func parseSrc(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, parser.SkipObjectResolution)
	require.NoError(t, err, "parse synthetic source")
	return f
}

// countRunDispatches counts CallExpr whose callee is the bare identifier `Run`
// (the archtest.Run dispatch — each is one packages.Load).
func countRunDispatches(root ast.Node) int {
	n := 0
	EachInSubtree[ast.CallExpr](root, func(call *ast.CallExpr) {
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "Run" {
			n++
		}
	})
	return n
}

// countFuncDecls counts top-level FuncDecls named name.
func countFuncDecls(root ast.Node, name string) int {
	n := 0
	EachInChildren[ast.FuncDecl](root, func(fd *ast.FuncDecl) {
		if fd.Name != nil && fd.Name.Name == name {
			n++
		}
	})
	return n
}
