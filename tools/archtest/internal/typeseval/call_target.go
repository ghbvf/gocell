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
//     or *types.TypeName with a non-nil owning *types.Package. Covers both
//     dot-imported function references (e.g. `Sleep` after `import . "time"`)
//     and dot-imported type references (e.g. `ImportBan{}` after
//     `import . ".../scanner"`). *types.TypeName is the object kind for struct
//     types, interfaces, type aliases, and named types — all forms that appear
//     at the bare-Ident position when a type is referenced from a dot-import.
//
// Returns ("", "", false) for:
//
//   - vars, consts, builtins, and packages at the bare-Ident position
//     (*types.Var / *types.Const / *types.Builtin / *types.PkgName are not handled)
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
// bare-Ident matches for a locally-defined func or type return the current
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
		return resolveBarePkgSymbol(typesInfo, e)
	default:
		return "", "", false
	}
}

// resolveBarePkgSymbol resolves a bare *ast.Ident to (pkgPath, name) when the
// ident refers to a package-level *types.Func or *types.TypeName. Returns
// ("", "", false) for vars, consts, builtins, packages, and universe objects
// (nil Pkg). Extracted to keep ResolvePackageRef's cognitive complexity ≤15.
func resolveBarePkgSymbol(info *types.Info, id *ast.Ident) (pkgPath, name string, ok bool) {
	obj := info.Uses[id]
	switch sym := obj.(type) {
	case *types.Func:
		if sym.Pkg() == nil {
			return "", "", false
		}
		return sym.Pkg().Path(), sym.Name(), true
	case *types.TypeName:
		if sym.Pkg() == nil {
			return "", "", false
		}
		return sym.Pkg().Path(), sym.Name(), true
	default:
		return "", "", false
	}
}
