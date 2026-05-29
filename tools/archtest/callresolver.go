// Package archtest callresolver.go — façade re-exports for the callsite
// resolution convenience layer (internal/callresolver).
//
// This completes the façade contract for callsite-resolution: business
// archtest *_test.go files reach the walker + resolution helpers through
// archtest.{WalkFuncDecls,WalkFuncDeclsAST,IsCallToPkgFunc,HasReceiver},
// never importing internal/callresolver directly. PASS-FUNNEL-RESOLVE-01
// (pass_funnel_test.go) bans the direct-import path.
//
// The *Pass-taking walker (WalkFuncDecls) lives here rather than in
// internal/callresolver because Pass is declared in package archtest: an
// internal package referencing *archtest.Pass would form an import cycle
// (archtest already imports internal/callresolver to re-export it). Both
// walker forms delegate to the single decoupled engine
// internal/callresolver.WalkFuncDecls, so there is exactly one traversal
// implementation.
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/ghbvf/gocell/tools/archtest/internal/callresolver"
)

// FuncDeclContext is the per-FuncDecl visitor context yielded by
// [WalkFuncDecls] / [WalkFuncDeclsAST]. Type alias to
// [callresolver.FuncDeclContext] so the façade and internal engine share one
// type (mirrors the ImportBan = scanner.ImportBan alias in resolve.go).
type FuncDeclContext = callresolver.FuncDeclContext

// WalkFuncDecls iterates p.Files; for each top-level *ast.FuncDecl with a
// non-nil Body it invokes fn with a [FuncDeclContext] bound to p.TypesInfo,
// p.Fset and p.Rel(file). Works in both AST-only mode (ctx.Info == nil, e.g.
// a Pass from archtest.Run) and typed mode (Pass from RunTyped).
//
// It does NOT auto-skip _test.go / generated files — the callback decides
// (callers already branch on ctx.Rel suffix and p.IsGenerated). See
// [callresolver.WalkFuncDecls] for the depth-1 / bodiless-skip contract.
func WalkFuncDecls(p *Pass, fn func(ctx FuncDeclContext)) {
	if p == nil || fn == nil {
		return
	}
	callresolver.WalkFuncDecls(p.Files, p.TypesInfo, p.Fset, p.Rel, fn)
}

// WalkFuncDeclsAST is the no-Pass, AST-only walker for rules that parse files
// standalone (go/parser, no driver / no *types.Info) — e.g. golden-source
// scans. ctx.Info is always nil. fn and the same depth-1 / bodiless-skip
// contract as [WalkFuncDecls] apply.
func WalkFuncDeclsAST(files []*ast.File, fset *token.FileSet, rel func(*ast.File) string, fn func(ctx FuncDeclContext)) {
	if fn == nil {
		return
	}
	callresolver.WalkFuncDecls(files, nil, fset, rel, fn)
}

// IsCallToPkgFunc reports whether call's callee resolves (import-alias /
// dot-import / generic-instantiation proof) to the package-level function
// (pkgPath, name). Thin delegation to [callresolver.IsCallToPkgFunc].
//
// info must come from the same packages.Load result that produced call —
// guaranteed when info is pass.TypesInfo. Returns false for method calls (use
// [ResolveMethodCall]), nil info, or nil call. In particular, a nil info
// yields false, so this is safe to call from an AST-only WalkFuncDeclsAST
// callback where ctx.Info == nil (it will simply never match).
func IsCallToPkgFunc(info *types.Info, call *ast.CallExpr, pkgPath, name string) bool {
	return callresolver.IsCallToPkgFunc(info, call, pkgPath, name)
}

// HasReceiver reports whether fn declares a receiver whose base type name
// equals typeName (handles *T / T / T[P] / T[P,Q]). Thin delegation to
// [callresolver.HasReceiver]. AST-only; no *types.Info needed.
func HasReceiver(fn *ast.FuncDecl, typeName string) bool {
	return callresolver.HasReceiver(fn, typeName)
}
