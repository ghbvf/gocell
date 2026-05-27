package cellgen

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// renderCellMetaLiteral renders a *metadata.CellMeta value as a Go source
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
//
// CELLGEN-LITERAL-FUNNEL-02: unexported. The sole caller is BuildCellSpec,
// which pre-renders this string into CellGenSpec.RenderedMetaLiteral so the
// template never accesses *metadata.CellMeta. Out-of-package callers cannot
// re-introduce a funcMap path that would let cell.tmpl hand-enumerate fields.
func renderCellMetaLiteral(cell *metadata.CellMeta) string {
	if cell == nil {
		return "&metadata.CellMeta{}"
	}
	var sb strings.Builder
	sb.WriteString("&metadata.CellMeta{\n")
	renderMetaFields(&sb, reflect.ValueOf(*cell), reflect.TypeOf(*cell), "\t")
	sb.WriteString("}")
	return sb.String()
}

// renderMetaFields iterates exported fields of a metadata struct value
// (CellMeta or SliceMeta) at the given indent level and writes non-zero
// non-dash-yaml fields to sb. Reflect-driven so a new exported yaml-tagged
// field on either struct auto-projects into the generated literal — closing the
// "struct gains a field but the hand-written printer silently drops it" drift
// class for BOTH cell_gen.go and slice_gen.go.
func renderMetaFields(sb *strings.Builder, v reflect.Value, t reflect.Type, indent string) {
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
// For CellVerifyMeta / SliceVerifyMeta (contain slices), it renders multi-line.
func renderInlineStruct(v reflect.Value, t reflect.Type, indent string) string {
	// Use reflect.Type identity (not string name) to avoid silent breakage on rename.
	switch t {
	case reflect.TypeOf(metadata.CellVerifyMeta{}):
		return renderCellVerifyMeta(v, indent)
	case reflect.TypeOf(metadata.SliceVerifyMeta{}):
		return renderSliceVerifyMeta(v.Interface().(metadata.SliceVerifyMeta), indent)
	}
	// Single-line struct: OwnerMeta, SchemaMeta
	return renderSingleLineStruct(v, t)
}

// renderSingleLineStruct renders a struct as `metadata.TypeName{Field: "val", ...}`
// on a single line. Used for OwnerMeta, SchemaMeta, L0DepMeta.
//
// Only reflect.String fields (including typed-string newtypes such as
// GoIdentifier) are supported. Any other Kind panics via
// panicregister.Approved to ensure future CellMeta field additions with
// non-string types surface immediately at development time rather than
// silently generating broken Go literals.
func renderSingleLineStruct(v reflect.Value, t reflect.Type) string {
	return fmt.Sprintf("metadata.%s%s", t.Name(), structFieldsBraced(v, t))
}

// structFieldsBraced renders an all-string struct's non-zero fields as
// `{Field: "val", ...}` (no type prefix), for use both standalone (with a
// type prefix prepended) and as elements inside a typed slice literal
// `[]metadata.T{{...}, ...}`. Non-string fields panic via panicregister.Approved
// (fail-loud) so a future struct field with a non-string type surfaces at
// development time rather than emitting broken Go.
func structFieldsBraced(v reflect.Value, t reflect.Type) string {
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
		if fv.Kind() != reflect.String {
			panic(panicregister.Approved(
				"cellgen-unsupported-singleline-field-type",
				errcode.Assertion(
					"cellgen literal printer: structFieldsBraced: unsupported field kind %s for field %s in %s;"+
						" add a case in renderFieldValue and extend the struct renderer",
					fv.Kind(), field.Name, t.Name()),
			))
		}
		parts = append(parts, fmt.Sprintf("%s: %q", field.Name, fv.String()))
	}
	return fmt.Sprintf("{%s}", strings.Join(parts, ", "))
}

// renderCellVerifyMeta renders a CellVerifyMeta as a multi-line block.
// It iterates all exported fields of CellVerifyMeta generically so that adding
// a new field to the struct is automatically covered without updating this
// function. Zero-value / empty-slice fields are omitted.
//
// Output format for a single leading []string field (e.g. Smoke):
//
//	metadata.CellVerifyMeta{Smoke: []string{
//		"smoke.x.startup",
//	}}
//
// Output format when multiple non-zero fields are present (expanded per field):
//
//	metadata.CellVerifyMeta{
//		FieldA: []string{...},
//		FieldB: "...",
//	}
//
// The single-field compact style matches the existing cell_gen.go golden so that
// regeneration produces 0 diff for the current CellVerifyMeta definition.
// Unknown field types panic via panicregister.Approved (fail-loud).
func renderCellVerifyMeta(v reflect.Value, indent string) string {
	t := v.Type()

	// Collect non-zero fields first.
	type fieldEntry struct {
		sf reflect.StructField
		fv reflect.Value
	}
	var nonZero []fieldEntry
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		fv := v.Field(i)
		if isZeroValue(fv) {
			continue
		}
		nonZero = append(nonZero, fieldEntry{sf, fv})
	}

	if len(nonZero) == 0 {
		return "metadata.CellVerifyMeta{}"
	}

	inner := indent + "\t"

	// Compact single-field []string form: keeps backward-compatible output
	// so regenerating cell_gen.go produces 0 diff for the current struct.
	if len(nonZero) == 1 && nonZero[0].fv.Kind() == reflect.Slice &&
		nonZero[0].fv.Type().Elem().Kind() == reflect.String {
		sf := nonZero[0].sf
		fv := nonZero[0].fv
		var sb strings.Builder
		fmt.Fprintf(&sb, "metadata.CellVerifyMeta{%s: ", sf.Name)
		sb.WriteString(renderStringSlice(fv, inner))
		sb.WriteString("}")
		return sb.String()
	}

	// General expanded form for multiple fields or non-[]string first field.
	var sb strings.Builder
	sb.WriteString("metadata.CellVerifyMeta{\n")
	for _, e := range nonZero {
		sb.WriteString(inner)
		sb.WriteString(e.sf.Name)
		sb.WriteString(": ")
		sb.WriteString(renderFieldValue(e.fv, e.sf, inner))
		sb.WriteString(",\n")
	}
	sb.WriteString(indent)
	sb.WriteString("}")
	return sb.String()
}

// renderSlice renders a []string or []L0DepMeta field as a multi-line slice literal.
func renderSlice(fv reflect.Value, field reflect.StructField, indent string) string {
	elemType := fv.Type().Elem()

	switch elemType.Kind() { //nolint:exhaustive // only string and all-string structs (L0DepMeta / ContractUsage)
	case reflect.String:
		return renderStringSlice(fv, indent)
	case reflect.Struct:
		// Generic all-string struct slice: []metadata.L0DepMeta / []metadata.ContractUsage.
		// Elements render as type-elided braces {Field: "v", ...} (the slice type
		// declares the element type), zero-value fields omitted.
		return renderStructSlice(fv, elemType, indent)
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

// renderStructSlice renders an all-string struct slice as:
//
//	[]metadata.<ElemType>{
//		{Field: "x", ...},
//	}
//
// Generic over the element type (L0DepMeta / ContractUsage); per-element fields
// use the type-elided braced form from structFieldsBraced.
func renderStructSlice(fv reflect.Value, elemType reflect.Type, indent string) string {
	inner := indent + "\t"
	var sb strings.Builder
	fmt.Fprintf(&sb, "[]metadata.%s{\n", elemType.Name())
	for i := 0; i < fv.Len(); i++ {
		sb.WriteString(inner)
		sb.WriteString(structFieldsBraced(fv.Index(i), elemType))
		sb.WriteString(",\n")
	}
	sb.WriteString(indent)
	sb.WriteString("}")
	return sb.String()
}

// renderSliceMetaLiteral renders a *metadata.SliceMeta as a Go source literal
// `&metadata.SliceMeta{...}`. slice.yaml is the single source of truth for
// slice identity / consistency level / lifecycle — codegen projects every
// contract-bearing field so that the typed sliceMeta in slice_gen.go is the
// funnel SoR.
//
// Reflect-driven via the shared renderMetaFields (same path as
// renderCellMetaLiteral), so a new exported yaml-tagged SliceMeta field
// (e.g. Lifecycle) auto-projects without editing this function — the
// hand-enumerated form that previously dropped new fields is gone. Zero-value /
// empty-slice fields are omitted to keep regenerated output minimal.
//
// CELLGEN-LITERAL-FUNNEL-02 also applies: unexported. The sole caller is
// BuildSliceSpec, which writes the result into SliceGenSpec.RenderedMetaLiteral.
func renderSliceMetaLiteral(s *metadata.SliceMeta) string {
	if s == nil {
		return "&metadata.SliceMeta{}"
	}
	var sb strings.Builder
	sb.WriteString("&metadata.SliceMeta{\n")
	renderMetaFields(&sb, reflect.ValueOf(*s), reflect.TypeOf(*s), "\t")
	sb.WriteString("}")
	return sb.String()
}

// renderSliceVerifyMeta renders a metadata.SliceVerifyMeta inline at the given
// indent. Empty fields are omitted.
func renderSliceVerifyMeta(v metadata.SliceVerifyMeta, indent string) string {
	inner := indent + "\t"
	var sb strings.Builder
	sb.WriteString("metadata.SliceVerifyMeta{\n")
	if len(v.Unit) > 0 {
		fmt.Fprintf(&sb, "%sUnit: []string{\n", inner)
		for _, u := range v.Unit {
			fmt.Fprintf(&sb, "%s\t%q,\n", inner, u)
		}
		fmt.Fprintf(&sb, "%s},\n", inner)
	}
	if len(v.Contract) > 0 {
		fmt.Fprintf(&sb, "%sContract: []string{\n", inner)
		for _, c := range v.Contract {
			fmt.Fprintf(&sb, "%s\t%q,\n", inner, c)
		}
		fmt.Fprintf(&sb, "%s},\n", inner)
	}
	if len(v.Waivers) > 0 {
		fmt.Fprintf(&sb, "%sWaivers: []metadata.WaiverMeta{\n", inner)
		for _, w := range v.Waivers {
			fmt.Fprintf(&sb, "%s\t{Contract: %q, Owner: %q, Reason: %q, ExpiresAt: %q},\n",
				inner, w.Contract, w.Owner, w.Reason, w.ExpiresAt)
		}
		fmt.Fprintf(&sb, "%s},\n", inner)
	}
	sb.WriteString(indent)
	sb.WriteString("}")
	return sb.String()
}
