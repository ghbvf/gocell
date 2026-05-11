package typeseval

import (
	"go/ast"
	"go/types"
)

// ResolveMethodCall returns the *types.Func that a method-call SelectorExpr
// `recv.Method()` or method-expression `T.Method(recv, ...)` resolves to,
// using info.Selections to recover the actual method object regardless of
// how the call site reaches it. Handles:
//
//   - Direct interface receiver:     `var x fs.ReadDirFS; x.ReadDir(...)`
//   - Pointer / value method:        `f := os.Open(...); f.ReadDir(-1)`
//   - Promoted via struct embed:     `type W struct{ fs.ReadDirFS }; w.ReadDir(...)`
//   - Named type definition:         `type MyFS fs.ReadDirFS; var x MyFS; x.ReadDir(...)`
//   - Type alias:                    `type MyFS = fs.ReadDirFS; x.ReadDir(...)`
//   - Generic type parameter:        `func [F fs.ReadDirFS](x F) { x.ReadDir(...) }`
//   - Method expression (qualified): `fs.ReadDirFS.ReadDir(fsys, ".")`
//   - Method expression (pointer):   `(*os.File).ReadDir(f, -1)`
//
// Callers filter by the resolved method's owning package and name:
//
//	fn, ok := typeseval.ResolveMethodCall(info, sel)
//	if !ok { return }
//	if banned[fn.Pkg().Path()] && contains(banned[fn.Pkg().Path()], fn.Name()) {
//	    // forbidden method call
//	}
//
// Returns (nil, false) for:
//
//   - non-method selectors (qualified `pkg.Func` is in info.Uses, not Selections;
//     use ResolvePackageRef for that shape)
//   - field-position selectors (info.Selections[sel].Kind() == FieldVal)
//   - methods whose owning *types.Package is nil (universe pseudo-types)
//   - nil typesInfo or nil sel
//
// ref: golang/tools go/types/typeutil.Callee (same info.Selections lookup pattern)
// ref: dominikh/go-tools analysis/code.IsCallTo (Selections.Obj() typed filter)
func ResolveMethodCall(typesInfo *types.Info, sel *ast.SelectorExpr) (*types.Func, bool) {
	if typesInfo == nil || sel == nil {
		return nil, false
	}
	s, ok := typesInfo.Selections[sel]
	if !ok {
		return nil, false
	}
	// Accept both MethodVal (recv.Method()) and MethodExpr (T.Method(recv, ...));
	// reject FieldVal. Both method-kinds carry the same method *types.Func via
	// Obj(); only the call-site syntax (and arity) differ.
	if s.Kind() != types.MethodVal && s.Kind() != types.MethodExpr {
		return nil, false
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok || fn.Pkg() == nil {
		return nil, false
	}
	return fn, true
}
