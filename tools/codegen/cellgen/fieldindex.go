package cellgen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"

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
// wired to the cell-struct field whose type is *X.T.
//
// Ambiguity is deferred, not fail-fast at index time: a package selector that
// appears on more than one field maps to the sentinel ambiguousField ("").
// Most duplicate selectors are harmless infrastructure packages (auth, cas,
// query, slog) that are never queried as slice IDs. A duplicate only matters
// when a SUBSCRIBE slice's own package is ambiguous (e.g. sessionlogout has
// both a *sessionlogout.Handler route field and a *sessionlogout.Consumer
// subscribe field); resolveSliceField reports that at query time and the
// subscribe CU must then carry an explicit `field:` to disambiguate.
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
			if _, dup := idx[pkg]; dup {
				idx[pkg] = ambiguousField // mark ambiguous; only an error if queried
				continue
			}
			idx[pkg] = name
		}
	}
	return idx, nil
}

// ambiguousField is the sentinel stored in the field index when a package
// selector appears on more than one struct field.
const ambiguousField = ""

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

// resolveSliceField returns the cell struct field name that holds a subscribe
// slice's consumer.
//
// explicitField (the slice.yaml subscribe CU `field:`) wins when set — the
// generated `c.<field>.<handler>` expression is compile-checked by the Go build,
// so an invalid name surfaces at compile time. This is the disambiguator for
// slices that own more than one cell-struct field (e.g. sessionlogout: a route
// Handler field plus a subscribe Consumer field).
//
// Otherwise the field is resolved by convention from fieldIndex[sliceID]:
//   - absent     → error (cell.go lacks a *sliceID.T pointer field)
//   - ambiguous  → error (multiple *sliceID.T fields; add `field:` to the CU)
func resolveSliceField(fieldIndex map[string]string, explicitField, cellID, sliceID string) (string, error) {
	if explicitField != "" {
		return explicitField, nil
	}
	if fieldIndex == nil {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: fieldIndex is nil; IndexCellStructFields must run before BuildCellSpec when subscriptions exist",
			errcode.WithDetails(slog.String("cellID", cellID)))
	}
	field, ok := fieldIndex[sliceID]
	if !ok {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: no cell.go struct field for subscribing slice; "+
				"cell struct must declare a *<sliceID>.T pointer field, or set field: on the subscribe CU",
			errcode.WithDetails(
				slog.String("sliceID", sliceID),
				slog.String("cellID", cellID),
			))
	}
	if field == ambiguousField {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: subscribing slice has multiple *<sliceID>.T cell-struct fields; set field: on the subscribe CU to disambiguate",
			errcode.WithDetails(
				slog.String("sliceID", sliceID),
				slog.String("cellID", cellID),
			))
	}
	return field, nil
}
