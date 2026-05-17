// INVARIANT: SCAFFOLD-DERIVED-FORCEOVERWRITE-01
//
// Every derived-codegen pathsafe.PlannedFile carrying ForceOverwrite=true in
// tools/codegen/cellgen MUST be constructed by planDerivedArtifact
// (stage_render.go). planDerivedArtifact is the sole site that restores the
// governance.IsGoCellGenerated overwrite gate which the legacy codegen.Write
// enforced before PR #544 routed derived writes through the pathsafe
// single-plan funnel. A direct pathsafe.PlannedFile{...ForceOverwrite:true}
// composite literal anywhere else in cellgen would silently overwrite a
// hand-written (non-generated) file on the project tree — exactly the F1
// regression this rule locks shut.
//
// AI-rebust: Medium (downstream caller-allowlist, type-aware via go/types —
// the literal's type is resolved to pkg/pathsafe.PlannedFile, not matched by
// name). Upstream is Soft: pathsafe.PlannedFile.ForceOverwrite is an exported
// field that pathsafe's cross-package callers set directly, so "skip the
// constructor" cannot be made unrepresentable at the type level here.
// Upstream Hard-ization is tracked by backlog
// PATHSAFE-FORCEOVERWRITE-TYPED-CTOR-01 (cap-14), same Lane E typed-scaffold
// single-source收编 as PATHSAFE-PLANSET-TYPED-HARD-01.
//
// # Recognition: type-aware
//
// A CompositeLit matches only when info.Types[lit.Type] resolves to the
// *types.Named (github.com/ghbvf/gocell/pkg/pathsafe).PlannedFile. Alias
// imports / dot-imports of pathsafe therefore cannot evade the rule.
//
// # Blind spots (declared per ai-collab §载体决策原则)
//
// EachInSubtree[ast.CompositeLit] + KeyValueExpr value inspection does NOT
// reason about:
//
//  1. ForceOverwrite set to a non-literal expression that evaluates true at
//     runtime (e.g. `ForceOverwrite: someBoolVar`). The main rule keys on the
//     `true` *ast.Ident literal only.
//  2. The benign propagation form `ForceOverwrite: f.ForceOverwrite` in
//     materializeSkeletonStage (skeleton plan entries never set force=true
//     themselves — they originate in scaffold.go / scaffold_bundle.go which
//     never set the field — so copying the field is a no-op-true).
//
// TestScaffoldDerivedForceOverwrite_NoNonLiteralForms is the reverse
// self-check: it asserts the production cellgen AST contains ONLY the two
// expected ForceOverwrite value forms (literal `true` inside
// planDerivedArtifact; selector `f.ForceOverwrite` inside
// materializeSkeletonStage). Any third form — including a bool variable used
// to sneak true past blind spot #1 — fails the self-check.
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

const (
	pathsafePkgPath       = "github.com/ghbvf/gocell/pkg/pathsafe"
	plannedFileTypeName   = "PlannedFile"
	forceOverwriteField   = "ForceOverwrite"
	derivedCtorFuncName   = "planDerivedArtifact"
	skeletonStageFuncName = "materializeSkeletonStage"
	cellgenRelPrefix      = "tools/codegen/cellgen/"
)

// isPlannedFileType reports whether expr resolves to pathsafe.PlannedFile.
func isPlannedFileType(info *types.Info, expr ast.Expr) bool {
	if info == nil || expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == pathsafePkgPath && obj.Name() == plannedFileTypeName
}

// forceOverwriteValue returns the value expression of a ForceOverwrite
// KeyValueExpr in lit, or nil if the field is not explicitly set.
func forceOverwriteValue(lit *ast.CompositeLit) ast.Expr {
	var val ast.Expr
	EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
		if val != nil {
			return
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != forceOverwriteField {
			return
		}
		val = kv.Value
	})
	return val
}

func isLiteralTrue(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "true"
}

// TestScaffoldDerivedForceOverwrite_OnlyInConstructor enforces
// SCAFFOLD-DERIVED-FORCEOVERWRITE-01: a pathsafe.PlannedFile composite literal
// with `ForceOverwrite: true` in tools/codegen/cellgen is allowed only inside
// func planDerivedArtifact.
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
				EachInSubtree[ast.CompositeLit](fn.Body, func(lit *ast.CompositeLit) {
					if !isPlannedFileType(p.TypesInfo, lit.Type) {
						return
					}
					v := forceOverwriteValue(lit)
					if v == nil || !isLiteralTrue(v) {
						return
					}
					if fn.Name != nil && fn.Name.Name == derivedCtorFuncName {
						return
					}
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(lit.Pos()).Line,
						Message: "SCAFFOLD-DERIVED-FORCEOVERWRITE-01: pathsafe.PlannedFile{ForceOverwrite:true} " +
							"outside planDerivedArtifact — derived writes must go through the " +
							"governance.IsGoCellGenerated overwrite gate (stage_render.go)",
					})
				})
			})
		}
		return out
	})
	Report(t, "SCAFFOLD-DERIVED-FORCEOVERWRITE-01", diags)
}

// TestScaffoldDerivedForceOverwrite_NoNonLiteralForms is the declared
// blind-spot reverse self-check (see file godoc): the ONLY ForceOverwrite
// value forms permitted in production cellgen AST are
//
//   - literal `true` inside planDerivedArtifact
//   - selector `f.ForceOverwrite` inside materializeSkeletonStage
//
// Anything else (a bool variable, a function call, a literal true outside the
// constructor) fails — closing the "sneak true via non-literal" blind spot.
func TestScaffoldDerivedForceOverwrite_NoNonLiteralForms(t *testing.T) {
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
				fnName := ""
				if fn.Name != nil {
					fnName = fn.Name.Name
				}
				EachInSubtree[ast.CompositeLit](fn.Body, func(lit *ast.CompositeLit) {
					if !isPlannedFileType(p.TypesInfo, lit.Type) {
						return
					}
					v := forceOverwriteValue(lit)
					if v == nil {
						return // field omitted → zero value false, fine
					}
					if isLiteralTrue(v) && fnName == derivedCtorFuncName {
						return
					}
					if sel, ok := v.(*ast.SelectorExpr); ok &&
						sel.Sel != nil && sel.Sel.Name == forceOverwriteField &&
						fnName == skeletonStageFuncName {
						return // benign skeleton propagation
					}
					if id, ok := v.(*ast.Ident); ok && id.Name == "false" {
						return // explicit false → harmless
					}
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(v.Pos()).Line,
						Message: "SCAFFOLD-DERIVED-FORCEOVERWRITE-01: unexpected ForceOverwrite value form in " +
							fnName + " — only literal true (planDerivedArtifact) or f.ForceOverwrite " +
							"propagation (materializeSkeletonStage) are permitted",
					})
				})
			})
		}
		return out
	})
	Report(t, "SCAFFOLD-DERIVED-FORCEOVERWRITE-01", diags)
}
