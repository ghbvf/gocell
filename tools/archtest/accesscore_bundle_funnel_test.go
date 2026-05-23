// INVARIANT: ACCESSCORE-BUNDLE-FUNNEL-01
//
// # ACCESSCORE-BUNDLE-FUNNEL-01
//
// Composition roots must wire accesscore via the typed bundle funnel
// (WithMemBundle / WithPGBundle). The散装 options
// WithUserRepository / WithRoleRepository / WithSetupLock / WithTxManager
// MUST NOT be exported from the cells/accesscore package: keeping them
// unexported makes a misconfiguration (e.g. wiring UserRepository from one
// store and TxRunner from another) inexpressible at compile time outside
// the cell's own package.
//
// # AI-robust: Hard (Go visibility)
//
// The Hard defense is the Go visibility rule itself:
//   - Bundle struct fields are private — composite literals from outside the
//     cells/accesscore package cannot construct a MemBundle / PGBundle.
//   - withUserRepository / withRoleRepository / withSetupLock / withTxManager
//     are unexported — call sites outside cells/accesscore cannot reach them.
//
// This archtest is the *reverse self-check*: it scans the production source
// of the cell package for any re-exported variant (e.g. someone restores
// WithUserRepository as an exported function) and fails the build before
// the regression escapes review.
//
// # Blind spots
//
//   - BS-1: aliased export. `func WithUserRepository = withUserRepository`
//     would be an exported variable, not a function. AST scan for exported
//     ValueSpec covers this.
//   - BS-2: reflection / unsafe. Out of scope per ai-robust.md §3.
package archtest

import (
	"go/ast"
	"strings"
	"testing"
)

const accesscoreBundleFunnelRuleID = "ACCESSCORE-BUNDLE-FUNNEL-01"

// accesscoreBundleForbiddenExports is the closed set of names that must NOT
// appear as exported (capitalized) function or value declarations in the
// cells/accesscore package root. Bundle funnel collapses them to
// unexported call sites only.
var accesscoreBundleForbiddenExports = map[string]struct{}{
	"WithUserRepository": {},
	"WithRoleRepository": {},
	"WithSetupLock":      {},
	"WithTxManager":      {},
}

// TestAccessCoreBundleFunnel scans cells/accesscore for any production
// declaration whose name matches accesscoreBundleForbiddenExports. After the
// Bundle migration these names must only exist as unexported (lowercase)
// helpers called from WithMemBundle / WithPGBundle inside the same package.
func TestAccessCoreBundleFunnel(t *testing.T) {
	diags := RunTyped(t,
		TypedOpts{Tests: false},
		[]string{"./cells/accesscore"},
		scanAccesscoreBundleFunnelViolations,
	)
	Report(t, accesscoreBundleFunnelRuleID, diags)
}

func scanAccesscoreBundleFunnelViolations(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if fn.Recv != nil {
				return
			}
			if _, banned := accesscoreBundleForbiddenExports[fn.Name.Name]; !banned {
				return
			}
			line := p.Fset.Position(fn.Name.Pos()).Line
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "exported wiring " + fn.Name.Name +
					" must be unexported — Bundle funnel is the only public wire path",
			})
		})
		EachInSubtree[ast.ValueSpec](file, func(vs *ast.ValueSpec) {
			for _, name := range vs.Names {
				if _, banned := accesscoreBundleForbiddenExports[name.Name]; !banned {
					continue
				}
				line := p.Fset.Position(name.Pos()).Line
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: "exported wiring symbol " + name.Name +
						" must be unexported — Bundle funnel is the only public wire path",
				})
			}
		})
	}
	return out
}
