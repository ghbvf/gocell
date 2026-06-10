//go:build archtest

// INVARIANT: ARCHTEST-SINGLE-RUN-ENTRY-01
//
// ARCHTEST-SINGLE-RUN-ENTRY-01 enforces that the archtest package exposes
// exactly ONE exported entry-point function whose name begins with "Run":
// [Run] itself and [RunStandardCellRules] (the external-cell convenience
// entry). All formerly-public Run* variants (RunTyped, RunTypedProduction,
// RunTypedDir, RunTypedFixture) were deleted in issue #1037 §1d and MUST NOT
// reappear.
//
// # AI-robust: Medium
//
// Go has no mechanism at the language level that prevents declaring a new
// exported func named RunFoo in a package — only a compiler error from a
// duplicate declaration can make that truly Hard. This archtest is therefore
// Medium: it provides a CI backstop that fires the moment any `func Run<X>`
// is declared in a tools/archtest/ facade file (non-test .go files only).
//
// The Hard upstream is the sealed [RunScope] interface: the only meaningful
// parameter that used to distinguish RunTyped / RunTypedDir /
// RunTypedProduction / RunTypedFixture is now expressed as a constructor call
// inside the sealed RunScope value, making the separate entry points
// expressively unnecessary. Re-adding RunTyped would be a no-op in terms of
// what callers can express, so this archtest's Medium backstop is sufficient
// to prevent silent drift.
//
// # Blind spots (per ai-robust.md Medium evidence requirement)
//
//   - A new Run* entry point declared under a different func type — a method
//     (`func (x X) RunFoo()`) or a package-level func-typed var, whether a
//     closure (`var RunFoo = func(){}`) or a function-value alias
//     (`var RunFoo = Run`) — is not detected by the MAIN scan, which only visits
//     top-level receiver-less FuncDecls. This blind spot has its own reverse
//     self-check: TestArchtestSingleRunEntry_BlindSpotProbe (types-bound, so it
//     catches the alias too) asserts none of these forms occur in the façade
//     (currently vacuous), so introducing one fires the probe.
//   - This scan covers only direct-child non-test .go files of
//     tools/archtest/ (the façade boundary). Sub-packages are not scanned
//     (they are inaccessible to business archtest authors who import only
//     "tools/archtest").
//
// Reverse self-check: the test verifies that [Run] and [RunStandardCellRules]
// ARE present (non-empty allowlist) so a future removal of the main entry
// point also fails CI; TestArchtestSingleRunEntry_BlindSpotProbe covers the
// method / var-func (closure + function-value alias) blind-spot forms.
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// runEntryAllowlist is the exact set of exported function names starting with
// "Run" that are permitted in the archtest façade. Any name starting with "Run"
// outside this set is a violation.
var runEntryAllowlist = map[string]bool{
	"Run":                  true, // single unified entry point (pass.go)
	"RunStandardCellRules": true, // external-cell convenience wrapper (external.go)
}

// TestArchtestSingleRunEntry enforces ARCHTEST-SINGLE-RUN-ENTRY-01: there is
// no exported function whose name starts with "Run" in the archtest façade
// except [Run] and [RunStandardCellRules].
func TestArchtestSingleRunEntry(t *testing.T) {
	diags := Run(t, AST(facadeScopeForArchtest(t)), func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			// EachInChildren over *ast.File visits top-level Decls (sanctioned
			// scanner walker; SCANNER-FRAMEWORK-USAGE-01 forbids raw for-range).
			EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				// Only top-level exported functions (no receiver).
				if fn.Recv != nil || fn.Name == nil || !fn.Name.IsExported() {
					return
				}
				name := fn.Name.Name
				if !strings.HasPrefix(name, "Run") {
					return
				}
				if runEntryAllowlist[name] {
					return
				}
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(fn.Name.Pos()).Line,
					Message: "exported func " + name + " starts with \"Run\" but is not in the " +
						"archtest single-Run-entry allowlist {Run, RunStandardCellRules}; " +
						"use Run(t, <RunScope>, rule) with the appropriate RunScope constructor " +
						"(AST / Typed / Production / Fixture / StandaloneModule) — " +
						"ARCHTEST-SINGLE-RUN-ENTRY-01",
				})
			})
		}
		return out
	})

	Report(t, "ARCHTEST-SINGLE-RUN-ENTRY-01", diags)

	// Reverse self-check: confirm the allowlist members are actually present
	// so removing Run or RunStandardCellRules also fails CI.
	found := make(map[string]bool)
	Run(t, AST(facadeScopeForArchtest(t)), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Recv == nil && fn.Name != nil && fn.Name.IsExported() {
					found[fn.Name.Name] = true
				}
			})
		}
		return nil
	})
	for name := range runEntryAllowlist {
		if !found[name] {
			t.Errorf("ARCHTEST-SINGLE-RUN-ENTRY-01 reverse self-check: "+
				"expected façade func %q to be present but it was not found — "+
				"the allowlist entry is stale or the function was accidentally removed",
				name)
		}
	}
}

// TestArchtestSingleRunEntry_BlindSpotProbe is the reverse self-check for the
// first documented blind spot of ARCHTEST-SINGLE-RUN-ENTRY-01: a Run* entry
// declared under a func type the main scan does not cover — a method
// (`func (x X) RunFoo()`) or a package-level var of FUNCTION type, whether a
// closure (`var RunFoo = func(){...}`) or a function-value ALIAS
// (`var RunFoo = Run`). The main test only scans top-level receiver-less
// FuncDecls, so all of these slip past it.
//
// The var leg is types-bound, not "value is a func literal": a Run* alias
// `var RunFoo = Run` binds the existing Run function value and has no FuncLit
// node, so an AST-only literal check would miss it. Resolving each declared name
// through *types.Info and keeping only those whose type underlying is
// *types.Signature catches both the closure and the alias while excluding a
// non-callable `var RunCount int`. The probe therefore loads the façade typed
// (Typed over ./tools/archtest, non-test = the facadeScopeForArchtest file set).
//
// Per ai-robust.md (each blind spot needs a reverse self-check asserting it does
// NOT occur in production), this probe asserts zero. It is currently vacuous (no
// such forms exist), which is exactly the point: a future method or var-func
// Run* entry fires this probe — not the main rule — keeping the blind spot from
// going silently live.
func TestArchtestSingleRunEntry_BlindSpotProbe(t *testing.T) {
	type hit struct {
		rel, name, form string
		line            int
	}
	var hits []hit
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./tools/archtest"}), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)
			// Method-receiver form: exported Run* method.
			EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Recv == nil || fn.Name == nil || !fn.Name.IsExported() {
					return
				}
				if !strings.HasPrefix(fn.Name.Name, "Run") || runEntryAllowlist[fn.Name.Name] {
					return
				}
				hits = append(hits, hit{
					rel, fn.Name.Name, "method",
					p.Fset.Position(fn.Name.Pos()).Line,
				})
			})
			// Var-func form: package-level exported Run* var whose type is a
			// function signature — a closure OR a function-value alias. go/types
			// (not an AST FuncLit check) is required to catch `var RunFoo = Run`.
			// EachInChildren[ast.ValueSpec] is the sanctioned depth-1 walk over
			// GenDecl.Specs (SCANNER-FRAMEWORK-USAGE-01 funnel).
			EachInChildren[ast.GenDecl](f, func(gd *ast.GenDecl) {
				if gd.Tok != token.VAR {
					return
				}
				EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
					// Match only the declared names (vs.Names), resolved via
					// *types.Info — ranging vs.Names over []*ast.Ident is
					// SCANNER-FRAMEWORK-USAGE-01-safe (no AST-list type assertion).
					for _, name := range vs.Names {
						if !name.IsExported() || !strings.HasPrefix(name.Name, "Run") ||
							runEntryAllowlist[name.Name] {
							continue
						}
						obj := p.TypesInfo.Defs[name]
						if obj == nil {
							continue
						}
						if _, isFunc := obj.Type().Underlying().(*types.Signature); !isFunc {
							continue
						}
						hits = append(hits, hit{
							rel, name.Name, "var-func",
							p.Fset.Position(name.Pos()).Line,
						})
					}
				})
			})
		}
		return nil
	})
	for _, h := range hits {
		t.Errorf("ARCHTEST-SINGLE-RUN-ENTRY-01 blind-spot probe: %s:%d declares %s Run* entry %q — "+
			"the main rule only scans top-level receiver-less FuncDecls, so this form would slip past it; "+
			"express it as Run(t, <RunScope>, rule) instead", h.rel, h.line, h.form, h.name)
	}
}
