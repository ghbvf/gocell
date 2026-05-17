// Package funcdecl_mixed_red is the RED fixture for fix-6 FuncDecl-level
// granularity of FIXTURESPEC-COUNT-MATCH-ENFORCED-01.
//
// The file contains two top-level FuncDecls:
//
//   - runA: declares a hardcoded `wantLines []int` struct field AND calls a
//     fixture loader (archtest.RunTyped), but has NO inline AssertDiagnosticCount
//     or NoDiagnosticAssertion. The current file-level rule exempts runA
//     because runB elsewhere in the file calls AssertDiagnosticCount — that
//     exemption leaks. Wave 2 FuncDecl-level rule must flag runA.
//
//   - runB: declares the same loader call AND calls AssertDiagnosticCount
//     inline. Must NOT be flagged either before or after Wave 2.
//
// This fixture intentionally references the real archtest package (via go.mod
// replace) so callee resolution lands on the same pkgPath the funnel checks
// (`github.com/ghbvf/gocell/tools/archtest`); a stub package would not match
// because the rule resolves via *types.Info, not name matching.
package funcdecl_mixed_red

import (
	"github.com/ghbvf/gocell/tools/archtest"
)

func runA() {
	// Inline anonymous struct mirrors the real table-driven test anti-pattern:
	// the wantLines []int field lives inside the FuncDecl body, exactly the
	// shape FIXTURESPEC-COUNT-MATCH-ENFORCED-01 targets.
	cases := []struct {
		wantLines []int
	}{
		{wantLines: []int{1, 2}},
	}
	_ = cases
	archtest.RunTyped(nil, archtest.TypedOpts{}, nil, func(p *archtest.Pass) []archtest.Diagnostic {
		return nil
	})
}

func runB() {
	archtest.RunTyped(nil, archtest.TypedOpts{}, nil, func(p *archtest.Pass) []archtest.Diagnostic {
		archtest.AssertDiagnosticCount(nil, "RUNB", p, nil)
		return nil
	})
}
