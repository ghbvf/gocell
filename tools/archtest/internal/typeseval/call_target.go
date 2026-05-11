package typeseval

import (
	"go/ast"
	"go/types"
)

// ResolvePackageRef returns the (pkgPath, name) tuple for a reference to a
// package-level symbol, covering two AST shapes:
//
//   - Qualified selector `pkg.Name` — requires info.Uses[sel.X] to resolve to
//     *types.PkgName. Sel.Name is taken syntactically; the symbol itself does
//     NOT need to resolve through types.Info. This tolerates partial type
//     info, e.g. fixtures that type-check with importer.Default() against
//     packages it cannot load (the import alias still resolves to *types.PkgName
//     even when the imported package's symbols don't).
//
//   - Bare identifier `Name` — requires info.Uses[id] to resolve to *types.Func
//     with a non-nil owning *types.Package. Used to identify dot-imported
//     function references (where the syntactic Name carries no package info,
//     so types.Info is the only source of truth).
//
// Returns ("", "", false) for:
//
//   - non-function objects (vars, types, consts, builtins, packages) at the
//     bare-Ident position
//   - method-position selectors (`receiver.Method` where sel.X is a value)
//   - identifiers whose owning *types.Package is nil (universe builtins)
//   - nil typesInfo or nil expr
//   - any expression kind other than *ast.Ident or *ast.SelectorExpr —
//     wrappers like *ast.ParenExpr, *ast.IndexExpr (generic instantiation
//     `Func[T]`), and *ast.IndexListExpr (`Func[T, U]`) are NOT unwrapped;
//     callers iterating via scanner.EachInSubtree pick up the underlying
//     Ident/SelectorExpr nodes directly, but a caller that passes a wrapper
//     gets ("", "", false)
//
// Callers are responsible for filtering by pkgPath / name. In particular,
// bare-Ident matches for a locally-defined function return the current
// package's path; matchers that only care about cross-package references must
// check pkgPath explicitly.
//
// ref: golang/tools go/analysis/passes/copylock/copylock.go (qualified identifier resolution via info.Uses[id].(*types.PkgName))
// ref: dominikh/go-tools analysis/code/code.go (TypesInfo lookup pattern with explicit nil guards)
func ResolvePackageRef(typesInfo *types.Info, expr ast.Expr) (pkgPath, name string, ok bool) {
	if typesInfo == nil || expr == nil {
		return "", "", false
	}
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		xIdent, isIdent := e.X.(*ast.Ident)
		if !isIdent || e.Sel == nil {
			return "", "", false
		}
		pkgName, isPkg := typesInfo.Uses[xIdent].(*types.PkgName)
		if !isPkg {
			return "", "", false
		}
		return pkgName.Imported().Path(), e.Sel.Name, true
	case *ast.Ident:
		fn, isFunc := typesInfo.Uses[e].(*types.Func)
		if !isFunc || fn.Pkg() == nil {
			return "", "", false
		}
		return fn.Pkg().Path(), fn.Name(), true
	default:
		return "", "", false
	}
}
