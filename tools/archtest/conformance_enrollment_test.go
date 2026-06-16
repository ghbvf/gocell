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
		file    = "conformance_enrollment.go"
		coreFn  = "loadConformanceEnrollmentImpls"
		runName = "Run"
	)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	require.NoError(t, err, "parse %s", file)

	var runCalls, coreFnDecls int
	EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name != nil && fd.Name.Name == coreFn {
			coreFnDecls++
		}
	})
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == runName {
			runCalls++
		}
	})

	// Anti-vacuity: the core function must exist (else the guard scans the wrong
	// file and silently passes).
	require.Equal(t, 1, coreFnDecls,
		"expected exactly one %s declaration in %s (anti-vacuity); the guard is scanning the wrong file or the core was renamed",
		coreFn, file)

	// The single-load invariant: exactly one Run dispatch across the whole file.
	// loadConformanceEnrollmentImpls holds it; the repo/saga family check bodies
	// delegate to that core and issue no direct load.
	assert.Equal(t, 1, runCalls,
		"CONFORMANCE-ENROLLMENT-SINGLE-LOAD-01: %s must contain exactly ONE archtest.Run "+
			"(the folded single Tests=true load, #2249); got %d. A second load reintroduces the "+
			"halved-away nightly load cost — keep iface resolution + impl collection + test scan in one load.",
		file, runCalls)
}
