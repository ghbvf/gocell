package cellgen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// IndexCellStructFields parses the Go source file at cellGoPath and returns a
// map from the short package name of pointer-typed struct fields to their field
// names.
//
// Only *pkg.Type fields (StarExpr → SelectorExpr → Ident) are indexed.
// Non-pointer fields, embedded (anonymous) fields, and fields whose type is not
// a qualified selector (e.g. plain same-package types) are silently ignored.
//
// The returned map lets BuildCellSpec resolve "sliceID → cell struct field
// name" for subscription HandlerExpr generation. By convention the slice
// package short name equals the slice ID in GoCell, so a subscribe slice X is
// wired to the unique cell-struct field whose type is *X.T.
//
// Fail-fast on ambiguity: if two fields share the same package selector the
// resolution would be non-deterministic, so an error is returned rather than
// silently picking one. The 0-match case is reported later by resolveSliceField
// at the point of use (it knows the slice/cell IDs).
//
// Example: for `projectionSvc *orderprojection.Service` the entry is
// idx["orderprojection"] = "projectionSvc".
func IndexCellStructFields(cellGoPath string) (map[string]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, cellGoPath, nil, 0)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"cellgen fieldindex: parse cell.go", err)
	}

	idx := make(map[string]string)
	for _, st := range structTypes(f) {
		for _, field := range st.Fields.List {
			pkg, name, ok := fieldPkgName(field)
			if !ok {
				continue
			}
			if existing, dup := idx[pkg]; dup {
				return nil, fmt.Errorf(
					"cellgen fieldindex: package %q maps to both fields %q and %q in %s — "+
						"subscription field resolution requires a unique *%s.T field",
					pkg, existing, name, cellGoPath, pkg)
			}
			idx[pkg] = name
		}
	}
	return idx, nil
}

// structTypes returns every struct type declaration in f, in source order.
func structTypes(f *ast.File) []*ast.StructType {
	var out []*ast.StructType
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if st, ok := typeSpec.Type.(*ast.StructType); ok {
				out = append(out, st)
			}
		}
	}
	return out
}

// fieldPkgName extracts the package selector and field name from a named
// *pkg.Type struct field. Returns ok=false for anonymous, non-pointer, or
// non-selector fields.
func fieldPkgName(field *ast.Field) (pkg, name string, ok bool) {
	if len(field.Names) == 0 {
		return "", "", false
	}
	star, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return "", "", false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	return pkgIdent.Name, field.Names[0].Name, true
}

// resolveSliceField looks up the cell struct field name for a given slice ID
// using the prebuilt fieldIndex. Returns an error when the slice ID is absent
// from the index (the cell.go struct lacks a *sliceID.T pointer field — a
// structural mismatch).
func resolveSliceField(fieldIndex map[string]string, cellID, sliceID string) (string, error) {
	if fieldIndex == nil {
		return "", fmt.Errorf("cellgen build: fieldIndex is nil for cell %q — "+
			"IndexCellStructFields must be called before BuildCellSpec when subscriptions exist",
			cellID)
	}
	field, ok := fieldIndex[sliceID]
	if !ok {
		return "", fmt.Errorf("cellgen build: no cell.go struct field for slice %q in cell %q "+
			"(cell struct must have a *%s.T pointer field for subscription HandlerExpr generation)",
			sliceID, cellID, sliceID)
	}
	return field, nil
}
