package cellgen

import (
	"go/ast"
	"go/parser"
	"go/token"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// CellFieldIndex indexes the pointer fields of a cell.go cell struct so that a
// subscribe CU resolves to a concrete, typed cell-struct field before cellgen
// renders the `c.<field>.<handler>` expression. It is the typed target the
// reviewer's "resolve to a determinate typed target before rendering" direction
// requires: a field reference never reaches code generation as a bare name.
//
// Two complementary lookups, both derived from the SAME single cell struct:
//
//   - byPkg:   slice package short name → field name. Drives convention
//     resolution (slice package short name == sliceID). A package that appears
//     on more than one field maps to ambiguousField ("").
//   - byField: field name → slice package short name. Validates an explicit
//     slice.yaml `field:` actually points at the SUBSCRIBING slice's own
//     package (byField[field] == sliceID), so a misdirected `field:` fails
//     generation instead of silently binding the subscription to a different
//     slice whose type happens to expose a same-named method.
type CellFieldIndex struct {
	byPkg   map[string]string
	byField map[string]string
}

// IndexCellStructFields parses the Go source at cellGoPath and indexes the
// *pkg.Type pointer fields of the single struct named cellStructName (the cell
// struct named by cell.yaml goStructName).
//
// Only that one struct is scanned: helper / option / config structs declared
// in the same cell.go cannot pollute the index or manufacture false ambiguity
// (e.g. an options struct that also holds a *orderprojection.Foo field must not
// shadow the cell struct's own field, nor flip a package to ambiguous).
//
// Only *pkg.Type fields (StarExpr → SelectorExpr → Ident) are indexed.
// Non-pointer fields, embedded (anonymous) fields, and fields whose type is not
// a qualified selector (plain same-package types) are ignored. When
// cellStructName is not declared in the file the returned index is empty (a
// subscribing slice then fails with a descriptive "no cell.go struct field"
// error from resolveSliceField).
//
// Example: for the cell struct `type AccessCore struct { projectionSvc
// *orderprojection.Service }`, byPkg["orderprojection"] == "projectionSvc" and
// byField["projectionSvc"] == "orderprojection".
func IndexCellStructFields(cellGoPath, cellStructName string) (*CellFieldIndex, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, cellGoPath, nil, 0)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"cellgen fieldindex: parse cell.go", err)
	}

	idx := &CellFieldIndex{
		byPkg:   make(map[string]string),
		byField: make(map[string]string),
	}
	st := findStruct(f, cellStructName)
	if st == nil {
		return idx, nil
	}
	for _, field := range st.Fields.List {
		pkg, name, ok := fieldPkgName(field)
		if !ok {
			continue
		}
		// Field names are unique within a struct, so byField never collides.
		idx.byField[name] = pkg
		if _, dup := idx.byPkg[pkg]; dup {
			idx.byPkg[pkg] = ambiguousField // mark ambiguous; only an error if queried by convention
			continue
		}
		idx.byPkg[pkg] = name
	}
	return idx, nil
}

// ambiguousField is the sentinel stored in byPkg when a package selector
// appears on more than one struct field.
//
// The empty string is safe as a sentinel because a valid field name can never
// be empty: goLocalIdentPattern (^[a-zA-Z_][A-Za-z0-9_]*$) requires at least
// one character, so resolveSliceField's map-ok-idiom unambiguously distinguishes
// "key absent" (ok=false) from "key present but ambiguous" (ok=true, value="").
const ambiguousField = ""

// findStruct returns the struct type declared as `type <name> struct {...}` in
// f, or nil when no such struct exists.
func findStruct(f *ast.File, name string) *ast.StructType {
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name == nil || typeSpec.Name.Name != name {
				continue
			}
			if st, ok := typeSpec.Type.(*ast.StructType); ok {
				return st
			}
		}
	}
	return nil
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

// resolveSliceField returns the cell struct field name that holds a slice's
// consumer/handler, for the generated `c.<field>.<expr>` expression. It is
// shared by every contractUsage role that renders a cell-struct field reference
// (subscribe, webhook-receive, webhook-dispatch); the role is carried through
// to error diagnostics so a webhook author is not pointed at a "subscribe CU"
// that does not exist in their slice.yaml.
//
// explicitField (the slice.yaml CU `field:`) when set MUST name a
// *<sliceID>.T pointer field on the cell struct — byField[explicitField] must
// equal sliceID. A field that exists but points at a different slice's package
// is rejected: otherwise `c.<field>.<expr>` could compile (when that other
// type happens to expose a same-named method) and silently bind the
// registration to the wrong slice. This binds the configuration reference to a
// determinate typed target before rendering, rather than trusting the Go build
// to incidentally catch it. It is the disambiguator for slices owning more than
// one *sliceID.T field (e.g. sessionlogout: a route Handler plus a subscribe
// Consumer).
//
// Otherwise the field is resolved by package convention from byPkg[sliceID]:
//   - absent     → error (cell.go lacks a *sliceID.T pointer field)
//   - ambiguous  → error (multiple *sliceID.T fields; add field: to the CU)
func (idx *CellFieldIndex) resolveSliceField(explicitField, cellID, sliceID, role string) (string, error) {
	if idx == nil {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: fieldIndex is nil; IndexCellStructFields must run before BuildCellSpec when field-bound contractUsages exist",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("role", role),
			))
	}
	if explicitField != "" {
		pkg, ok := idx.byField[explicitField]
		if !ok {
			return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"cellgen build: field: names no *<pkg>.T pointer field on the cell struct; "+
					"field: must reference a declared cell-struct field whose type is *<sliceID>.T",
				errcode.WithDetails(
					errcode.PublicString("sliceID", sliceID),
					errcode.PublicString("cellID", cellID),
					errcode.PublicString("role", role),
					errcode.PublicString("field", explicitField),
				))
		}
		if pkg != sliceID {
			return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"cellgen build: field: points to a different slice's package, not the owning slice; "+
					"the generated c.<field>.<expr> would bind the registration to the wrong slice",
				errcode.WithDetails(
					errcode.PublicString("sliceID", sliceID),
					errcode.PublicString("cellID", cellID),
					errcode.PublicString("role", role),
					errcode.PublicString("field", explicitField),
					errcode.PublicString("fieldPackage", pkg),
				))
		}
		return explicitField, nil
	}
	field, ok := idx.byPkg[sliceID]
	if !ok {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: no cell.go struct field for slice; "+
				"cell struct must declare a *<sliceID>.T pointer field, or set field: on the contractUsage",
			errcode.WithDetails(
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("role", role),
			))
	}
	if field == ambiguousField {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: slice has multiple *<sliceID>.T cell-struct fields; set field: on the contractUsage to disambiguate",
			errcode.WithDetails(
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("role", role),
			))
	}
	return field, nil
}
