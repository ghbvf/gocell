// INVARIANT: SCAFFOLD-DERIVED-FORCEOVERWRITE-01
//
// Every production reference to pathsafe.DerivedOverwrite must occur inside
// tools/codegen/cellgen/stage_render.go::planDerivedArtifact — the sole site
// that restores the governance.IsGoCellGenerated overwrite gate.
// planDerivedArtifact is the SOLE production caller of the typed
// pathsafe.DerivedOverwrite constructor in the entire repository.
//
// AI-rebust: Hard (compile-time + archtest funnel).
//
//   - **Upstream Hard (compile-time)**:
//     pkg/pathsafe.PlannedFile.forceOverwrite is package-private and the only
//     public path that produces a force-overwrite PlannedFile is
//     pathsafe.DerivedOverwrite. A composite literal anywhere outside the
//     pathsafe package cannot set forceOverwrite — the Go compiler rejects
//     the field-name reference. This is the upstream half of the funnel
//     (PATHSAFE-FORCEOVERWRITE-TYPED-CTOR-01) and needs no archtest because
//     the type system itself enforces it.
//
//   - **Downstream Hard (archtest)**: every CallExpr that resolves via
//     *types.Info to pkg/pathsafe.DerivedOverwrite in **any production
//     package** must occur inside planDerivedArtifact. Any other call site
//     fails the archtest — a hand-rolled caller in any package that
//     bypassed the governance.IsGoCellGenerated gate would be detected
//     even though the compiler accepts the typed constructor in isolation.
//     Tests are excluded so fixture code can still exercise DerivedOverwrite
//     directly.
//
// # Recognition: type-aware (Ident + SelectorExpr unified)
//
// `pathsafe.DerivedOverwrite(...)` parses as `*ast.SelectorExpr` whose
// `.Sel` is the function name. Alias imports (`p "pkg/pathsafe"; p.DerivedOverwrite()`)
// keep the same SelectorExpr shape. **Dot-import** (`import . "pkg/pathsafe";
// DerivedOverwrite()`) collapses to a bare `*ast.Ident`, NOT a SelectorExpr —
// so the archtest must resolve **both** Ident and SelectorExpr through
// *types.Info.Uses to the underlying *types.Func. callsDerivedOverwrite
// implements this unified resolution.
//
// # Blind spots (declared per ai-collab §载体决策原则)
//
//  1. Indirect call through a function-typed variable
//     (`f := pathsafe.DerivedOverwrite; f(...)`). TestScaffoldDerivedForceOverwrite_NoIndirectReference
//     covers this by rejecting any non-CallExpr reference to DerivedOverwrite
//     (either via SelectorExpr or dot-imported Ident).
//
//  2. A future caller written as `pathsafe.DerivedOverwrite(x, y)` could
//     pass the type check but produce content that bypasses the upstream
//     governance gate the cellgen-level planDerivedArtifact runs. The
//     archtest accepts that planDerivedArtifact is the trusted gatekeeper —
//     a Lane-E successor that splits the gate from the constructor must
//     re-validate this assumption.
//     Tracked: backlog SCAFFOLD-DERIVED-FORCEOVERWRITE-GATE-SPLIT-BACKLOG (cap-14).
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const (
	pathsafePkgPath     = "github.com/ghbvf/gocell/pkg/pathsafe"
	derivedOverwriteFn  = "DerivedOverwrite"
	derivedCtorFuncName = "planDerivedArtifact"
	// derivedCtorRel pins the single permitted call-site file. cellgen/ is
	// also matched as a defense-in-depth check that planDerivedArtifact
	// stays in stage_render.go (its godoc-declared home).
	derivedCtorRel = "tools/codegen/cellgen/stage_render.go"
)

// resolveDerivedOverwriteIdent returns the *types.Func bound to ident if it
// resolves through types.Info.Uses to pathsafe.DerivedOverwrite, otherwise
// nil. Handles both selector-form (pkg.DerivedOverwrite) and dot-import-form
// (DerivedOverwrite) — the caller passes whichever Ident represents the
// function name.
func resolveDerivedOverwriteIdent(info *types.Info, ident *ast.Ident) *types.Func {
	if info == nil || ident == nil || ident.Name != derivedOverwriteFn {
		return nil
	}
	obj, ok := info.Uses[ident]
	if !ok || obj == nil {
		return nil
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return nil
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != pathsafePkgPath {
		return nil
	}
	return fn
}

// callsDerivedOverwrite reports whether call.Fun resolves through types.Info
// to pathsafe.DerivedOverwrite. Handles BOTH:
//
//   - SelectorExpr (`pathsafe.DerivedOverwrite()` or aliased)
//   - Ident (dot-import form: `import . "pkg/pathsafe"; DerivedOverwrite()`)
//
// Either case routes through resolveDerivedOverwriteIdent → types.Info.Uses
// → *types.Func with Pkg().Path() == pathsafePkgPath, so the function name
// alone is never the discriminator (which would miss dot-import or be
// fooled by a same-name function in another package).
func callsDerivedOverwrite(info *types.Info, call *ast.CallExpr) bool {
	if info == nil || call == nil {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return resolveDerivedOverwriteIdent(info, fun.Sel) != nil
	case *ast.Ident:
		return resolveDerivedOverwriteIdent(info, fun) != nil
	default:
		return false
	}
}

// TestScaffoldDerivedForceOverwrite_OnlyInConstructor enforces
// SCAFFOLD-DERIVED-FORCEOVERWRITE-01 downstream: pathsafe.DerivedOverwrite
// in any production package may be called only from planDerivedArtifact
// (which lives in tools/codegen/cellgen/stage_render.go).
func TestScaffoldDerivedForceOverwrite_OnlyInConstructor(t *testing.T) {
	t.Parallel()

	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Body == nil {
					return
				}
				EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
					if !callsDerivedOverwrite(p.TypesInfo, call) {
						return
					}
					// Allow the sole gatekeeper: planDerivedArtifact in
					// stage_render.go.
					if fn.Name != nil && fn.Name.Name == derivedCtorFuncName &&
						rel == derivedCtorRel {
						return
					}
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(call.Pos()).Line,
						Message: "SCAFFOLD-DERIVED-FORCEOVERWRITE-01: pathsafe.DerivedOverwrite called outside " +
							"tools/codegen/cellgen/stage_render.go::planDerivedArtifact — " +
							"derived writes must go through the governance.IsGoCellGenerated overwrite gate",
					})
				})
			})
		}
		return out
	})
	Report(t, "SCAFFOLD-DERIVED-FORCEOVERWRITE-01", diags)
}

// TestScaffoldDerivedForceOverwrite_NoIndirectReference is the declared
// blind-spot reverse self-check (see file godoc blind spot #1): production
// AST in any package must NOT reference pathsafe.DerivedOverwrite outside a
// CallExpr position. A bare identifier reference (e.g. taking a function
// value via `f := pathsafe.DerivedOverwrite`) would bypass the
// _OnlyInConstructor archtest because it scans CallExpr only.
func TestScaffoldDerivedForceOverwrite_NoIndirectReference(t *testing.T) {
	t.Parallel()

	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			// SelectorExpr form: pathsafe.DerivedOverwrite as a non-call
			// reference.
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if resolveDerivedOverwriteIdent(p.TypesInfo, sel.Sel) == nil {
					return
				}
				if isCallExprFun(file, sel) {
					return
				}
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(sel.Pos()).Line,
					Message: "SCAFFOLD-DERIVED-FORCEOVERWRITE-01: indirect SelectorExpr reference to " +
						"pathsafe.DerivedOverwrite (function value / pointer) defeats the " +
						"caller-allowlist archtest — must always appear inside a direct CallExpr",
				})
			})
			// Dot-import Ident form: DerivedOverwrite as a non-call reference.
			EachInSubtree[ast.Ident](file, func(ident *ast.Ident) {
				if resolveDerivedOverwriteIdent(p.TypesInfo, ident) == nil {
					return
				}
				if isIdentCallExprFun(file, ident) || isInsideSelectorExpr(file, ident) {
					return
				}
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(ident.Pos()).Line,
					Message: "SCAFFOLD-DERIVED-FORCEOVERWRITE-01: indirect dot-imported Ident reference to " +
						"pathsafe.DerivedOverwrite defeats the caller-allowlist archtest — " +
						"must always appear inside a direct CallExpr",
				})
			})
		}
		return out
	})
	Report(t, "SCAFFOLD-DERIVED-FORCEOVERWRITE-01", diags)
}

// isCallExprFun walks file looking for any CallExpr whose Fun field is sel.
// Returns true iff sel appears as the Fun of some CallExpr in the file.
func isCallExprFun(file *ast.File, sel *ast.SelectorExpr) bool {
	found := false
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if found {
			return
		}
		if call.Fun == sel {
			found = true
		}
	})
	return found
}

// isIdentCallExprFun is the Ident analog of isCallExprFun: returns true iff
// ident appears as the Fun of some CallExpr in the file (dot-import form).
func isIdentCallExprFun(file *ast.File, ident *ast.Ident) bool {
	found := false
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if found {
			return
		}
		if call.Fun == ident {
			found = true
		}
	})
	return found
}

// isInsideSelectorExpr returns true iff ident appears as the Sel of any
// SelectorExpr in the file. SelectorExpr.Sel positions are already handled
// by the SelectorExpr walker; skip them here to avoid double-reporting.
func isInsideSelectorExpr(file *ast.File, ident *ast.Ident) bool {
	found := false
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if found {
			return
		}
		if sel.Sel == ident {
			found = true
		}
	})
	return found
}
