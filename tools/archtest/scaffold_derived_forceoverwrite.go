// Importable rule body for SCAFFOLD-DERIVED-FORCEOVERWRITE-01. Migrated from the
// legacy _test.go form to a non-test .go (M3 #1302) so external Cell repos can
// import and run it via StandardCellRules / RunStandardCellRules. The dogfood +
// RED-fixture precision gate live in scaffold_derived_forceoverwrite_test.go.
//
// # SCAFFOLD-DERIVED-FORCEOVERWRITE-01
//
// Every production reference to pathsafe.DerivedOverwrite must occur inside
// tools/codegen/cellgen/stage_render.go::planDerivedArtifact — the sole site
// that restores the governance.IsGoCellGenerated overwrite gate.
// planDerivedArtifact is the SOLE production caller of the typed
// pathsafe.DerivedOverwrite constructor in the entire repository.
//
// # AI-robust: Hard (compile-time + archtest funnel)
//
//   - Upstream Hard (compile-time): pkg/pathsafe.PlannedFile.forceOverwrite is
//     package-private and the only public path that produces a force-overwrite
//     PlannedFile is pathsafe.DerivedOverwrite. A composite literal outside the
//     pathsafe package cannot set forceOverwrite — the Go compiler rejects the
//     field-name reference (PATHSAFE-FORCEOVERWRITE-TYPED-CTOR-01).
//   - Downstream Hard (archtest): every CallExpr that resolves via *types.Info
//     to pkg/pathsafe.DerivedOverwrite in any production package must occur
//     inside planDerivedArtifact. Tests are excluded so fixture code can still
//     exercise DerivedOverwrite directly.
//
// # Recognition: type-aware (Ident + SelectorExpr unified)
//
// pathsafe.DerivedOverwrite(...) parses as *ast.SelectorExpr whose .Sel is the
// function name; alias imports keep the same shape. Dot-import collapses to a
// bare *ast.Ident. Both are resolved through *types.Info.Uses to the underlying
// *types.Func by resolveDerivedOverwriteIdent.
//
// # Blind spots (declared per ai-robust §载体决策原则)
//
//  1. Indirect call through a function-typed variable: covered by the reverse
//     scan (derivedIndirectViolations) which rejects any non-CallExpr reference.
//  2. A future caller written as a direct CallExpr could pass the type check but
//     produce content that bypasses the upstream governance gate
//     planDerivedArtifact runs — the archtest accepts planDerivedArtifact as the
//     trusted gatekeeper.
//
// Scan scope is the running module (Production → findModuleRoot), supplied by
// the driver; cfg.BuildTags adds tag-gated production files. The platform symbol
// path is derived from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01).
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const ruleScaffoldDerivedForceOverwrite01 = "SCAFFOLD-DERIVED-FORCEOVERWRITE-01"

const (
	// pathsafePkgPath is the import path of pkg/pathsafe, derived from
	// PlatformModulePath so a module rename / /v2 bump updates one place.
	pathsafePkgPath     = PlatformModulePath + "/pkg/pathsafe"
	derivedOverwriteFn  = "DerivedOverwrite"
	derivedCtorFuncName = "planDerivedArtifact"
	// derivedCtorRel pins the single permitted call-site file. It is a
	// running-module repo-relative path, NOT a platform module-path literal, so
	// an external Cell repo (which has no such file) makes the rule vacuous-green.
	derivedCtorRel = "tools/codegen/cellgen/stage_render.go"
)

// derivedOverwriteExternalNote is the consumer-action clause appended to every
// SCAFFOLD-DERIVED-FORCEOVERWRITE-01 diagnostic (forward call + both indirect
// reference forms) so an external Cell repo gets the same actionable message: in
// a consumer module there is no sanctioned site, so the fix is always to remove
// the reference — never to "make it a direct call" (which is also banned).
const derivedOverwriteExternalNote = " (pathsafe.DerivedOverwrite is a GoCell " +
	"platform-internal codegen primitive; external Cell code must never call it — " +
	"remove this reference)"

// CheckScaffoldDerivedForceOverwrite enforces SCAFFOLD-DERIVED-FORCEOVERWRITE-01
// downstream: pathsafe.DerivedOverwrite in any production package may be
// referenced only as a direct call from planDerivedArtifact
// (tools/codegen/cellgen/stage_render.go). It scans the running module's
// production code (Production → findModuleRoot), covering tag-gated files via
// cfg.BuildTags, and returns the diagnostics it observes.
//
// External Cell repo semantics (this rule is registered in StandardCellRules):
// the sanctioned planDerivedArtifact site lives in GoCell's own cellgen and does
// not exist in a consumer module, so the allowlist never matches there — the
// rule degrades to a PURE BAN. That is intended: pathsafe.DerivedOverwrite is a
// GoCell platform-internal codegen primitive with no valid use in external Cell
// code; a clean external repo simply has zero references (vacuous-green).
func CheckScaffoldDerivedForceOverwrite(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	// Scan twice per the ConfigForExternalCell.BuildTags contract: the default
	// build config first (so files behind //go:build !<tag> are not missed), then
	// the tagged config when cfg.BuildTags is non-empty (so files behind
	// //go:build <tag> are covered). A tagged-only load EXCLUDES default-only
	// files, so a single tagged pass would leave a hole — same default+tagged
	// shape as CheckPanicRegistered. scanner.Canonical dedups the overlap (an
	// unconstrained file is loaded by both passes).
	out := Run(t, Production(TypedOpts{}), collectDerivedOverwriteViolations)
	if len(cfg.BuildTags) > 0 {
		out = append(out, Run(t, Production(TypedOpts{Tags: cfg.BuildTags}), collectDerivedOverwriteViolations)...)
	}
	return scanner.Canonical(out)
}

// collectDerivedOverwriteViolations is the single per-Pass scanner shared by the
// production Check and the fixture precision gate (no parallel rule body). It
// combines the forward caller-allowlist check and the reverse indirect-reference
// check over each non-test file in the Pass.
func collectDerivedOverwriteViolations(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		out = append(out, derivedForwardViolations(p, file, rel)...)
		out = append(out, derivedIndirectViolations(p, file, rel)...)
	}
	return out
}

// isDerivedCtorSite reports whether (pkgPath, rel, fnName) identifies the single
// sanctioned planDerivedArtifact site in GoCell's own cellgen package.
//
// The allowlist is bound to the PLATFORM package path (cellgenPkgPath, derived
// from PlatformModulePath), not merely a repo-relative path + function name. A
// repo-relative path + name is NOT a trustworthy identity in a consumer module:
// without the package-path bind, an external Cell repo could recreate
// tools/codegen/cellgen/stage_render.go::planDerivedArtifact and get whitelisted,
// defeating the registered rule's pure-ban guarantee. A forged site in a consumer
// module has pkgPath = <consumer-module>/tools/codegen/cellgen ≠ cellgenPkgPath,
// so it is correctly NOT exempt. ref: go/analysis Pass carries Pkg identity for
// exactly this kind of provenance check.
func isDerivedCtorSite(pkgPath, rel, fnName string) bool {
	return pkgPath == cellgenPkgPath && rel == derivedCtorRel && fnName == derivedCtorFuncName
}

// derivedForwardViolations flags DerivedOverwrite CallExprs outside the
// sanctioned planDerivedArtifact@stage_render.go site.
func derivedForwardViolations(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
			if !callsDerivedOverwrite(p.TypesInfo, call) {
				return
			}
			if fn.Name != nil && p.Pkg != nil && isDerivedCtorSite(p.Pkg.Path(), rel, fn.Name.Name) {
				return
			}
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(call.Pos()).Line,
				Message: "SCAFFOLD-DERIVED-FORCEOVERWRITE-01: pathsafe.DerivedOverwrite called outside " +
					"tools/codegen/cellgen/stage_render.go::planDerivedArtifact — " +
					"derived writes must go through the governance.IsGoCellGenerated overwrite gate" +
					derivedOverwriteExternalNote,
			})
		})
	})
	return out
}

// derivedIndirectViolations flags references to DerivedOverwrite that are NOT the
// Fun of a direct CallExpr (function values / pointers), via both selector and
// dot-import Ident forms. SelectorExpr.Sel positions handled by the selector
// walker are skipped by the Ident walker to avoid double-reporting.
func derivedIndirectViolations(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
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
				"caller-allowlist archtest — must always appear inside a direct CallExpr" +
				derivedOverwriteExternalNote,
		})
	})
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
				"must always appear inside a direct CallExpr" +
				derivedOverwriteExternalNote,
		})
	})
	return out
}

// resolveDerivedOverwriteIdent returns the *types.Func bound to ident if it
// resolves through types.Info.Uses to pathsafe.DerivedOverwrite, otherwise nil.
// Handles both selector-form (pkg.DerivedOverwrite) and dot-import-form — the
// caller passes whichever Ident represents the function name.
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

// callsDerivedOverwrite reports whether call.Fun resolves through types.Info to
// pathsafe.DerivedOverwrite. Handles SelectorExpr (pkg.DerivedOverwrite() or
// aliased) and Ident (dot-import: DerivedOverwrite()).
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

// isCallExprFun reports whether sel appears as the Fun of some CallExpr in file.
//
// Identity is by AST node pointer (call.Fun == sel). This is exact for the
// single-parse AST the typed Run façade produces (go/parser yields one node
// instance per source position), which is the only mode this rule runs in. It
// would NOT hold for a cloned/transformed AST that copies nodes — a constraint
// that does not arise here. isIdentCallExprFun / isInsideSelectorExpr share the
// same single-parse-AST assumption.
func isCallExprFun(file *ast.File, sel *ast.SelectorExpr) bool {
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		return call.Fun == sel
	})
	return ok
}

// isIdentCallExprFun is the Ident analog of isCallExprFun (dot-import form).
func isIdentCallExprFun(file *ast.File, ident *ast.Ident) bool {
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		return call.Fun == ident
	})
	return ok
}

// isInsideSelectorExpr reports whether ident appears as the Sel of any
// SelectorExpr in file (already handled by the SelectorExpr walker).
func isInsideSelectorExpr(file *ast.File, ident *ast.Ident) bool {
	_, ok := FindFirstInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) bool {
		return sel.Sel == ident
	})
	return ok
}
