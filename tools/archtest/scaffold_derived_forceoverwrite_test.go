// INVARIANT: SCAFFOLD-DERIVED-FORCEOVERWRITE-01
//
// Every derived-codegen pathsafe.PlannedFile carrying force-overwrite
// semantics in tools/codegen/cellgen MUST be constructed by
// planDerivedArtifact (stage_render.go), which is the sole site that
// restores the governance.IsGoCellGenerated overwrite gate.
// planDerivedArtifact in turn is the SOLE production caller of the typed
// pathsafe.DerivedOverwrite constructor.
//
// AI-rebust: Hard (compile-time + archtest funnel).
//
//   - **Upstream Hard (compile-time)**:
//     pkg/pathsafe.PlannedFile.forceOverwrite is now package-private and the
//     only public path that produces a force-overwrite PlannedFile is
//     pathsafe.DerivedOverwrite. A composite literal anywhere outside the
//     pathsafe package cannot set forceOverwrite — the Go compiler rejects
//     the field-name reference. This is the upstream half of the funnel
//     (PATHSAFE-FORCEOVERWRITE-TYPED-CTOR-01) and needs no archtest because
//     the type system itself enforces it.
//
//   - **Downstream Hard (archtest)**: every CallExpr that resolves via
//     *types.Info to pkg/pathsafe.DerivedOverwrite under tools/codegen/cellgen
//     must occur inside planDerivedArtifact. Any other call site fails the
//     archtest — a hand-rolled caller that bypassed the
//     governance.IsGoCellGenerated gate would be detected even though the
//     compiler accepts the typed constructor in isolation. Tests are excluded
//     so fixture code can still exercise DerivedOverwrite directly.
//
// # Recognition: type-aware
//
// CallExpr matches only when info.Uses[CallExpr.Fun] resolves to the
// *types.Func (github.com/ghbvf/gocell/pkg/pathsafe).DerivedOverwrite. Alias
// imports / dot-imports of pathsafe therefore cannot evade the rule.
//
// # Blind spots (declared per ai-collab §载体决策原则)
//
// CallExpr scanning misses:
//
//  1. Indirect call through a function-typed variable
//     (`f := pathsafe.DerivedOverwrite; f(...)`). The reverse self-check
//     TestScaffoldDerivedForceOverwrite_NoIndirectReference rules this out
//     by also rejecting any non-CallExpr reference to DerivedOverwrite
//     (e.g. `_ = pathsafe.DerivedOverwrite`) in production cellgen AST.
//
//  2. A future caller written as `pathsafe.DerivedOverwrite(x, y)` could
//     pass the type check but produce content that bypasses the upstream
//     governance gate the cellgen-level planDerivedArtifact runs. The
//     archtest accepts that planDerivedArtifact is the trusted gatekeeper —
//     a Lane-E successor that splits the gate from the constructor must
//     re-validate this assumption.
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
	cellgenRelPrefix    = "tools/codegen/cellgen/"
)

// callsDerivedOverwrite reports whether call.Fun resolves through types.Info
// to pathsafe.DerivedOverwrite.
func callsDerivedOverwrite(info *types.Info, call *ast.CallExpr) bool {
	if info == nil || call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != derivedOverwriteFn {
		return false
	}
	obj, ok := info.Uses[sel.Sel]
	if !ok || obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != pathsafePkgPath {
		return false
	}
	return true
}

// TestScaffoldDerivedForceOverwrite_OnlyInConstructor enforces
// SCAFFOLD-DERIVED-FORCEOVERWRITE-01 downstream: pathsafe.DerivedOverwrite
// in tools/codegen/cellgen may be called only from planDerivedArtifact.
func TestScaffoldDerivedForceOverwrite_OnlyInConstructor(t *testing.T) {
	t.Parallel()

	diags := RunTyped(t, TypedOpts{}, []string{
		"./tools/codegen/cellgen/...",
	}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if !strings.HasPrefix(rel, cellgenRelPrefix) ||
				strings.HasSuffix(rel, "_test.go") {
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
					if fn.Name != nil && fn.Name.Name == derivedCtorFuncName {
						return
					}
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(call.Pos()).Line,
						Message: "SCAFFOLD-DERIVED-FORCEOVERWRITE-01: pathsafe.DerivedOverwrite called outside " +
							"planDerivedArtifact — derived writes must go through the " +
							"governance.IsGoCellGenerated overwrite gate (stage_render.go)",
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
// cellgen AST must NOT reference pathsafe.DerivedOverwrite outside a
// CallExpr position. A bare identifier reference (e.g. taking a function
// value via `f := pathsafe.DerivedOverwrite`) would bypass the
// _OnlyInConstructor archtest because it scans CallExpr only.
func TestScaffoldDerivedForceOverwrite_NoIndirectReference(t *testing.T) {
	t.Parallel()

	diags := RunTyped(t, TypedOpts{}, []string{
		"./tools/codegen/cellgen/...",
	}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if !strings.HasPrefix(rel, cellgenRelPrefix) ||
				strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if sel.Sel == nil || sel.Sel.Name != derivedOverwriteFn {
					return
				}
				obj, ok := p.TypesInfo.Uses[sel.Sel]
				if !ok || obj == nil {
					return
				}
				fn, ok := obj.(*types.Func)
				if !ok || fn.Pkg() == nil || fn.Pkg().Path() != pathsafePkgPath {
					return
				}
				// The CallExpr-context check is performed by the parent
				// walker: SelectorExpr appears as the Fun of a CallExpr in
				// legitimate usage. A SelectorExpr that is NOT a CallExpr.Fun
				// is an indirect reference.
				if isCallExprFun(file, sel) {
					return
				}
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(sel.Pos()).Line,
					Message: "SCAFFOLD-DERIVED-FORCEOVERWRITE-01: indirect reference to " +
						"pathsafe.DerivedOverwrite (function value / pointer) defeats the " +
						"caller-allowlist archtest — must always appear inside a direct CallExpr",
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
