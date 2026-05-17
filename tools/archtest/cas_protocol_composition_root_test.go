// INVARIANT: CAS-PROTOCOL-COMPOSITION-ROOT-01
//
// # CAS-PROTOCOL-COMPOSITION-ROOT-01
//
// cas.NewProtocol may only be invoked from cmd/* (composition root) or
// runtime/state/cas/* (the package itself). Cells, runtime/* (non-cas), and
// adapters/* must receive an injected *cas.Protocol — not construct one.
//
// # AI-rebust: Medium (type-aware)
//
// The rule resolves every CallExpr's callee through archtest.ResolvePackageRef,
// matching by the callee's owning package import path rather than the source-level
// Ident name. The Soft predecessor matched `sel.X.(*ast.Ident).Name == "cas"` on
// raw AST and missed two bypass forms:
//
//   - aliased import:  `import casPkg "…/runtime/state/cas"; casPkg.NewProtocol(...)`
//   - dot import:      `import . "…/runtime/state/cas"; NewProtocol(...)`
//
// archtest.ResolvePackageRef covers all three callee shapes in one resolution:
//   - SelectorExpr `pkg.Func` / `alias.Func` → info.Uses[sel.X].(*types.PkgName)
//   - bare Ident `Func` (dot-import)          → info.Uses[id].(*types.Func)
//
// Hard is architecturally unattainable for this rule's shape (caller-allowlist
// across multiple non-nested roots — cmd/, runtime/state/cas/, examples/):
// Go's internal/ package mechanic admits only a single owning subtree, so
// NewProtocol cannot be sealed to permit cmd/ + runtime/state/cas/* simultaneously
// without reshaping the typed Protocol injection paradigm. See SESSION-PROTOCOL-
// COMPOSITION-ROOT-01 for the same Hard-barrier analysis.
//
// # _test.go scope
//
// RunTyped(opts.Tests=false) loads only production-variant packages, so
// _test.go files are not in pass.Files. The scanner additionally filters by
// rel suffix for clarity — both gates are conservative.
//
// # Blind spots (BS)
//
//   - BS-1 Name shadowing: a non-cas package with a top-level function literally
//     named NewProtocol does NOT match (pkgPath comparison rejects it).
//     archtest.ResolvePackageRef returns the callee's owning package via
//     go/types, not the import-site alias.
//
//   - BS-2 Function-value indirection: `var fn = cas.NewProtocol; fn(...)` —
//     the `cas.NewProtocol` reference itself is a SelectorExpr that
//     ResolvePackageRef would match if it were a CallExpr.Fun. But the subsequent
//     `fn()` callee is a *types.Var (not *types.Func), so the resolver returns
//     ok=false. Accepted: the repo has no such pattern; this is the same accepted
//     blind spot as SESSION-PROTOCOL-COMPOSITION-ROOT-01 BS-2.
//
//   - BS-3 Reflection construction: out of scope per ai-collab.md §3.
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

const (
	casProtocolRuleID  = "CAS-PROTOCOL-COMPOSITION-ROOT-01"
	casPkgImportPath   = "github.com/ghbvf/gocell/runtime/state/cas"
	casOwnPkgRelPrefix = "runtime/state/cas/"
	casOwnPkgRelExact  = "runtime/state/cas"
)

// casProtocolForbidden is the closed set of cas-package constructors banned
// outside the composition root. (MustNewProtocol was deleted by B2-K-02;
// only NewProtocol remains.)
var casProtocolForbidden = map[string]struct{}{
	"NewProtocol": {},
}

// TestCASProtocol_CompositionRootOnly enforces CAS-PROTOCOL-COMPOSITION-ROOT-01:
// cas.NewProtocol may only be invoked from cmd/* (composition root) or
// runtime/state/cas/* (the package itself). Cells, runtime/* (non-cas), and
// adapters/* must consume an injected *cas.Protocol — not construct one.
//
// cmd/ and examples/ are intentionally outside the scan scope:
//   - cmd/* is the composition root by definition.
//   - examples/* each carry their own composition root; allowing them mirrors
//     the AUTH-PLAN-04 / LAYER-09 carve-out for example projects.
func TestCASProtocol_CompositionRootOnly(t *testing.T) {
	diags := RunTyped(t,
		TypedOpts{Tests: false},
		casProtocolProductionPatterns(),
		scanCASProtocolViolations,
	)
	Report(t, casProtocolRuleID, diags)
}

// casProtocolProductionPatterns returns the package patterns scanned by the
// production rule (cells / runtime / adapters).
func casProtocolProductionPatterns() []string {
	return []string{
		"./cells/...",
		"./runtime/...",
		"./adapters/...",
	}
}

// scanCASProtocolViolations walks every CallExpr in pass.Files, resolves the
// callee to its (pkgPath, name) tuple via archtest.ResolvePackageRef, and flags
// hits whose owning package is runtime/state/cas and whose name is in
// casProtocolForbidden.
//
// Two file-level filters apply:
//
//   - _test.go suffix: defense-in-depth alongside TypedOpts{Tests: false}.
//   - rel under "runtime/state/cas/": the package itself owns the constructor.
//
// Used by both TestCASProtocol_CompositionRootOnly (production scan, asserts
// zero diagnostics) and TestCASProtocol_RedFixtureDetected (fixture scan,
// asserts exactly 3 diagnostics across qualified / aliased / dot-import shapes).
func scanCASProtocolViolations(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		// Exempt the cas package itself and its sub-packages.
		if rel == casOwnPkgRelExact || strings.HasPrefix(rel, casOwnPkgRelPrefix) {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if !ok || pkgPath != casPkgImportPath {
				return
			}
			if _, banned := casProtocolForbidden[name]; !banned {
				return
			}
			line := p.Fset.Position(call.Pos()).Line
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"cas.%s must only be called from cmd/* (composition root) or runtime/state/cas/*; "+
						"cells / runtime (non-cas) / adapters must consume an injected *cas.Protocol",
					name),
			})
		})
	}
	return out
}
