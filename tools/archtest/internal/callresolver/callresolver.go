// Package callresolver is an archtest-internal convenience layer over
// [internal/typeseval] + [internal/scanner] for the recurring "walk every
// FuncDecl body, resolve a callee, assert a receiver" shape that several rules
// (audit_hash_input_frozen, serviceowned_handler_owner_check,
// changepassword_inactive_gate, …) previously hand-wrote and copy-drifted.
//
// Scope: archtest internal helper. Not exported beyond tools/archtest — the
// public surface is the re-exports in tools/archtest/callresolver.go
// (archtest.WalkFuncDecls / WalkFuncDeclsAST / IsCallToPkgFunc / HasReceiver).
// Business archtest *_test.go files MUST use that façade, never import this
// package directly — enforced by PASS-FUNNEL-RESOLVE-01 (pass_funnel_test.go).
//
// This package may import internal/typeseval and internal/scanner
// (internal↔internal is legal); it must NOT import package archtest, which
// would form an import cycle (archtest re-exports this package).
package callresolver

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// FuncDeclContext carries everything a per-FuncDecl visitor needs. In an
// AST-only walk Info is nil and Rel is "" (when no rel mapping was supplied).
type FuncDeclContext struct {
	File *ast.File
	Func *ast.FuncDecl
	Info *types.Info // nil in AST-only mode
	// Fset is the caller-supplied FileSet. It is non-nil whenever the caller
	// supplied one (archtest.WalkFuncDecls passes p.Fset); a callback that
	// reads ctx.Fset.Position(...) must therefore only be used with a walker
	// that was given a non-nil FileSet.
	Fset *token.FileSet
	Rel  string
}

// WalkFuncDecls is the decoupled engine behind archtest.WalkFuncDecls /
// WalkFuncDeclsAST. For each file it visits every top-level *ast.FuncDecl with
// a non-nil Body and invokes fn with a [FuncDeclContext]. rel maps a file to
// its module-relative slash path; a nil rel yields ctx.Rel == "".
//
// It deliberately does NOT auto-skip _test.go / generated files — skip policy
// differs per rule (some skip test files, some skip generated, some filter by
// directory), so the callback decides. Bodiless decls (external / forward
// declarations) are skipped because they cannot enclose statements.
//
// Uses scanner.EachInChildren[ast.FuncDecl] (depth-1) rather than a raw
// `range file.Decls` loop, keeping the engine itself SCANNER-FRAMEWORK-USAGE
// compliant. FuncDecls only exist at file top level (nested funcs are
// *ast.FuncLit, not FuncDecl), so depth-1 is complete.
func WalkFuncDecls(
	files []*ast.File,
	info *types.Info,
	fset *token.FileSet,
	rel func(*ast.File) string,
	fn func(FuncDeclContext),
) {
	if fn == nil {
		return
	}
	for _, f := range files {
		if f == nil {
			continue
		}
		r := ""
		if rel != nil {
			r = rel(f)
		}
		scanner.EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Body == nil {
				return
			}
			fn(FuncDeclContext{File: f, Func: fd, Info: info, Fset: fset, Rel: r})
		})
	}
}

// IsCallToPkgFunc reports whether call's callee resolves (import-alias /
// dot-import proof, generic-instantiation proof) to the package-level function
// (pkgPath, name). It strips generic-instantiation wrappers (*ast.IndexExpr /
// *ast.IndexListExpr) via [unwrapCallee] before delegating to
// [typeseval.ResolvePackageRef].
//
// Returns false for method calls (a SelectorExpr whose X is a value, not a
// package — use [typeseval.ResolveMethodCall] for those), nil info, or nil
// call.
func IsCallToPkgFunc(info *types.Info, call *ast.CallExpr, pkgPath, name string) bool {
	if info == nil || call == nil {
		return false
	}
	ref := unwrapCallee(call.Fun)
	if ref == nil {
		return false
	}
	p, n, ok := typeseval.ResolvePackageRef(info, ref)
	return ok && p == pkgPath && n == name
}

// unwrapCallee strips IndexExpr / IndexListExpr wrappers added by explicit
// generic type arguments (`Foo[T](...)` / `Foo[T, U](...)`) so the underlying
// SelectorExpr / Ident can be passed to ResolvePackageRef. Returns nil for any
// other callee shape. (Consolidated from serviceowned_handler_owner_check_test
// .go::unwrapCalleeForResolve.)
func unwrapCallee(fun ast.Expr) ast.Expr {
	switch v := fun.(type) {
	case *ast.SelectorExpr, *ast.Ident:
		return v
	case *ast.IndexExpr:
		return unwrapCallee(v.X)
	case *ast.IndexListExpr:
		return unwrapCallee(v.X)
	}
	return nil
}

// HasReceiver reports whether fn declares a receiver whose base type name
// equals typeName, handling *T / T / T[P] / T[P,Q] via
// [scanner.ReceiverTypeName]. Returns false for free functions (no receiver)
// or a nil fn.
func HasReceiver(fn *ast.FuncDecl, typeName string) bool {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return false
	}
	return scanner.ReceiverTypeName(fn.Recv.List[0].Type) == typeName
}
