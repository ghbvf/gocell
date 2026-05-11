package cellgen

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// RenderCellMetaLiteral renders a *metadata.CellMeta value as a Go source
// literal string of the form `&metadata.CellMeta{...}`. The output is valid
// Go that survives goimports+gofumpt without structural change.
//
// Field coverage is reflect-driven: every exported field with a non-"-" yaml
// tag is emitted if its value is non-zero. Fields tagged yaml:"-" (Dir, File)
// are always skipped. Zero-value / empty-slice fields are omitted so the
// rendered output matches the current cell_gen.go baseline.
//
// Type dispatch:
//   - string → %q
//   - metadata.GoIdentifier → metadata.MustNewGoIdentifier(%q)
//   - struct (OwnerMeta, SchemaMeta, CellVerifyMeta) → metadata.TypeName{...} inline
//   - []string → []string{\n\t\t"...",\n\t}
//   - []L0DepMeta → []metadata.L0DepMeta{{Cell: "...", Reason: "..."}, ...}
//
// Unknown reflect.Kind panics with panicregister.Approved (fail-loud so future
// CellMeta field additions surface immediately at development time).
func RenderCellMetaLiteral(cell *metadata.CellMeta) string {
	if cell == nil {
		return "&metadata.CellMeta{}"
	}
	var sb strings.Builder
	sb.WriteString("&metadata.CellMeta{\n")
	renderCellMetaFields(&sb, reflect.ValueOf(*cell), reflect.TypeOf(*cell), "\t")
	sb.WriteString("}")
	return sb.String()
}

// renderCellMetaFields iterates exported fields of a CellMeta value at the
// given indent level and writes non-zero non-dash-yaml fields to sb.
func renderCellMetaFields(sb *strings.Builder, v reflect.Value, t reflect.Type, indent string) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fv := v.Field(i)

		// Skip unexported fields (shouldn't happen in CellMeta, but defensive).
		if !field.IsExported() {
			continue
		}

		// Skip fields tagged yaml:"-".
		if isYAMLDash(field) {
			continue
		}

		// Skip zero-value fields (empty string, nil slice, zero struct, zero GoIdentifier).
		if isZeroValue(fv) {
			continue
		}

		sb.WriteString(indent)
		sb.WriteString(field.Name)
		sb.WriteString(": ")
		sb.WriteString(renderFieldValue(fv, field, indent))
		sb.WriteString(",\n")
	}
}

// isYAMLDash reports whether the field has yaml:"-" tag, meaning it should
// not appear in any YAML-derived output.
func isYAMLDash(field reflect.StructField) bool {
	tag := field.Tag.Get("yaml")
	return tag == "-" || strings.HasPrefix(tag, "-,")
}

// isZeroValue reports whether a reflect.Value is the zero value for its type,
// including nil slices and zero structs with all-zero fields.
func isZeroValue(v reflect.Value) bool {
	// metadata.GoIdentifier stores its value in an unexported field; use the
	// typed IsZero() method rather than reflecting exported fields.
	if v.Type() == reflect.TypeOf(metadata.GoIdentifier{}) {
		id := v.Interface().(metadata.GoIdentifier)
		return id.IsZero()
	}
	switch v.Kind() { //nolint:exhaustive // only handle types that appear in CellMeta
	case reflect.String:
		return v.String() == ""
	case reflect.Slice:
		return v.IsNil() || v.Len() == 0
	case reflect.Struct:
		// For struct fields, check if all exported sub-fields are zero.
		// This handles OwnerMeta{}, SchemaMeta{}, CellVerifyMeta{}.
		return isStructZero(v)
	default:
		return v.IsZero()
	}
}

// isStructZero reports whether all exported fields of a struct value are zero.
func isStructZero(v reflect.Value) bool {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if !t.Field(i).IsExported() {
			continue
		}
		fv := v.Field(i)
		if !isZeroValue(fv) {
			return false
		}
	}
	return true
}

// renderFieldValue returns the Go source literal for a single field value at
// the given indentation level.
func renderFieldValue(fv reflect.Value, field reflect.StructField, indent string) string {
	t := fv.Type()

	// metadata.GoIdentifier is a struct — handle before generic struct dispatch.
	if t == reflect.TypeOf(metadata.GoIdentifier{}) {
		id := fv.Interface().(metadata.GoIdentifier)
		return fmt.Sprintf("metadata.MustNewGoIdentifier(%q)", id.String())
	}

	switch fv.Kind() { //nolint:exhaustive // only handle types that appear in CellMeta
	case reflect.String:
		return fmt.Sprintf("%q", fv.String())
	case reflect.Struct:
		return renderInlineStruct(fv, t, indent)
	case reflect.Slice:
		return renderSlice(fv, field, indent)
	default:
		panic(panicregister.Approved(
			"cellgen-unsupported-field-type",
			errcode.Assertion("cellgen literal printer: unsupported field kind %s for field %s", fv.Kind(), field.Name),
		))
	}
}

// renderInlineStruct renders a named struct value as metadata.TypeName{key: val, ...}
// for structs where all field values fit on one line (OwnerMeta, SchemaMeta).
// For CellVerifyMeta (contains a slice), it renders multi-line.
func renderInlineStruct(v reflect.Value, t reflect.Type, indent string) string {
	typeName := t.Name()
	// CellVerifyMeta has a Smoke []string — render multi-line.
	if typeName == "CellVerifyMeta" {
		return renderCellVerifyMeta(v, indent)
	}
	// Single-line struct: OwnerMeta, SchemaMeta, L0DepMeta
	return renderSingleLineStruct(v, t)
}

// renderSingleLineStruct renders a struct as `metadata.TypeName{Field: "val", ...}`
// on a single line. Used for OwnerMeta, SchemaMeta, L0DepMeta.
func renderSingleLineStruct(v reflect.Value, t reflect.Type) string {
	var parts []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		fv := v.Field(i)
		if isZeroValue(fv) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %q", field.Name, fv.String()))
	}
	return fmt.Sprintf("metadata.%s{%s}", t.Name(), strings.Join(parts, ", "))
}

// renderCellVerifyMeta renders a CellVerifyMeta as a multi-line block:
//
//	metadata.CellVerifyMeta{Smoke: []string{
//		"smoke.x.startup",
//	}}
func renderCellVerifyMeta(v reflect.Value, indent string) string {
	// CellVerifyMeta has only one field: Smoke []string.
	smokeField := v.FieldByName("Smoke")
	if smokeField.IsNil() || smokeField.Len() == 0 {
		return "metadata.CellVerifyMeta{}"
	}
	inner := indent + "\t"
	var sb strings.Builder
	sb.WriteString("metadata.CellVerifyMeta{Smoke: []string{\n")
	for i := 0; i < smokeField.Len(); i++ {
		fmt.Fprintf(&sb, "%s%q,\n", inner, smokeField.Index(i).String())
	}
	sb.WriteString(indent)
	sb.WriteString("}}")
	return sb.String()
}

// renderSlice renders a []string or []L0DepMeta field as a multi-line slice literal.
func renderSlice(fv reflect.Value, field reflect.StructField, indent string) string {
	elemType := fv.Type().Elem()

	switch elemType.Kind() { //nolint:exhaustive // only string and L0DepMeta in CellMeta
	case reflect.String:
		return renderStringSlice(fv, indent)
	case reflect.Struct:
		if elemType == reflect.TypeOf(metadata.L0DepMeta{}) {
			return renderL0DepMetaSlice(fv, indent)
		}
		panic(panicregister.Approved(
			"cellgen-unsupported-field-type",
			errcode.Assertion("cellgen literal printer: unsupported slice element struct %s for field %s", elemType.Name(), field.Name),
		))
	default:
		panic(panicregister.Approved(
			"cellgen-unsupported-field-type",
			errcode.Assertion("cellgen literal printer: unsupported slice element kind %s for field %s", elemType.Kind(), field.Name),
		))
	}
}

// renderStringSlice renders a []string field as:
//
//	[]string{
//		"a",
//		"b",
//	}
func renderStringSlice(fv reflect.Value, indent string) string {
	inner := indent + "\t"
	var sb strings.Builder
	sb.WriteString("[]string{\n")
	for i := 0; i < fv.Len(); i++ {
		fmt.Fprintf(&sb, "%s%q,\n", inner, fv.Index(i).String())
	}
	sb.WriteString(indent)
	sb.WriteString("}")
	return sb.String()
}

// renderL0DepMetaSlice renders a []metadata.L0DepMeta field as:
//
//	[]metadata.L0DepMeta{
//		{Cell: "x", Reason: "y"},
//	}
func renderL0DepMetaSlice(fv reflect.Value, indent string) string {
	inner := indent + "\t"
	var sb strings.Builder
	sb.WriteString("[]metadata.L0DepMeta{\n")
	for i := 0; i < fv.Len(); i++ {
		elem := fv.Index(i)
		cell := elem.FieldByName("Cell").String()
		reason := elem.FieldByName("Reason").String()
		fmt.Fprintf(&sb, "%s{Cell: %q, Reason: %q},\n", inner, cell, reason)
	}
	sb.WriteString(indent)
	sb.WriteString("}")
	return sb.String()
}
