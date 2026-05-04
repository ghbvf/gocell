package markergen

import (
	"go/ast"
	"go/parser"
	"go/token"
)

// markerTarget indicates where a marker was found in the source.
type markerTarget int

const (
	typeLevel  markerTarget = iota // on the type/struct declaration
	fieldLevel                     // on a struct field
)

// collectedMarker holds a single parsed-but-not-yet-interpreted marker.
type collectedMarker struct {
	Name      string       // e.g. "cell:listener", "slice:route", "slice:subscribe"
	KVLine    string       // raw k=v,k=v string
	Line      int          // source line number
	Target    markerTarget // typeLevel or fieldLevel
	FieldName string       // non-empty for fieldLevel (struct field name, for error messages)
}

// CollectFromCellFile parses the Go source at path and returns all GoCell
// marker comments found on type declarations and struct fields.
//
// Only markers with known GoCell prefixes ("cell:" / "slice:") are returned;
// other "// +" markers (e.g. controller-gen, protoc) are silently skipped.
// Marker comments on func declarations are silently skipped; place markers on
// the struct field or type declaration only.
//
// ref: kubernetes-sigs/controller-tools pkg/markers/collect.go@main
func CollectFromCellFile(path string) ([]collectedMarker, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var markers []collectedMarker
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		markers = append(markers, collectFromGenDecl(fset, gd)...)
	}
	return markers, nil
}

// collectFromGenDecl extracts markers from a single type declaration block.
func collectFromGenDecl(fset *token.FileSet, gd *ast.GenDecl) []collectedMarker {
	var markers []collectedMarker
	// GenDecl-level doc comments (applies to all specs in a non-paren block).
	markers = append(markers, extractCommentMarkers(fset, gd.Doc, typeLevel, "")...)
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		// TypeSpec-level doc (used when the decl has parentheses).
		markers = append(markers, extractCommentMarkers(fset, ts.Doc, typeLevel, "")...)
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			continue
		}
		markers = append(markers, collectFromStructFields(fset, st)...)
	}
	return markers
}

// collectFromStructFields extracts field-level markers from a struct type.
func collectFromStructFields(fset *token.FileSet, st *ast.StructType) []collectedMarker {
	var markers []collectedMarker
	for _, field := range st.Fields.List {
		if field.Doc == nil {
			continue
		}
		fieldName := ""
		if len(field.Names) > 0 {
			fieldName = field.Names[0].Name
		}
		markers = append(markers, extractCommentMarkers(fset, field.Doc, fieldLevel, fieldName)...)
	}
	return markers
}

// extractCommentMarkers converts a comment group into collectedMarkers.
// Returns nil when cg is nil or contains no GoCell markers.
func extractCommentMarkers(fset *token.FileSet, cg *ast.CommentGroup, target markerTarget, fieldName string) []collectedMarker {
	if cg == nil {
		return nil
	}
	var markers []collectedMarker
	for _, c := range cg.List {
		name, kv, ok := splitMarker(c.Text)
		if !ok {
			continue
		}
		markers = append(markers, collectedMarker{
			Name:      name,
			KVLine:    kv,
			Line:      fset.Position(c.Pos()).Line,
			Target:    target,
			FieldName: fieldName,
		})
	}
	return markers
}
