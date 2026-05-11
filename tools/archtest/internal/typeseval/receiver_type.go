package typeseval

import "go/types"

// NamedTypeImportPath returns the import path of the package that declares
// the named type underlying t. Handles three type wrappings:
//
//   - *types.Pointer (one level): unwrap to element; `*os.File` → os.File
//   - *types.Named (the base case): return obj.Pkg().Path()
//   - *types.TypeParam (generic constraint): walk the constraint interface
//     for an embedded named type from any package, e.g.
//     `func [F fs.ReadDirFS](fsys F) { fsys.ReadDir(...) }` resolves the
//     receiver's static type *types.TypeParam to its bound fs.ReadDirFS
//
// Returns "" for:
//   - basic types, slices, maps, channels, structs, function types
//   - named types declared in the universe scope (e.g. error)
//   - nil or non-Named/Pointer/TypeParam shapes after one level of unwrapping
//
// Used by archtest matchers that filter method calls by receiver-type owning
// package (path A' in SCANNER-FRAMEWORK-USAGE-01). Companion to
// ResolvePackageRef which covers function/value references (paths A.2/A.3).
//
// ref: go/types Type interface — Pointer / Named / TypeParam shapes per spec.
func NamedTypeImportPath(t types.Type) string {
	if t == nil {
		return ""
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if tp, ok := t.(*types.TypeParam); ok {
		// types.TypeParam.Constraint() returns *types.Interface (possibly via Underlying for aliases).
		iface, ok := tp.Constraint().Underlying().(*types.Interface)
		if !ok {
			return ""
		}
		for i := 0; i < iface.NumEmbeddeds(); i++ {
			if path := NamedTypeImportPath(iface.EmbeddedType(i)); path != "" {
				return path
			}
		}
		return ""
	}
	named, ok := t.(*types.Named)
	if !ok {
		return ""
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	return obj.Pkg().Path()
}
