package contractgen

import (
	"bytes"
	"embed"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/types.ts.tmpl templates/barrel.ts.tmpl
var tsTemplatesFS embed.FS

// tsBarrelEntry is one line in the barrel index.ts:
//
//	export * as <Alias> from '<ImportPath>';
type tsBarrelEntry struct {
	// Alias is the camelCase namespace alias derived from the contract package path.
	// e.g. "httpOrderCreateV1"
	Alias string
	// ImportPath is the relative import path (without .ts extension).
	// e.g. "./contracts/http/order/create/v1/types"
	ImportPath string
}

// tsField is one field in a TS interface view.
type tsField struct {
	Key      string // wire key (BareJSONTag)
	Required bool
	Type     string // TS type expression
}

// tsEnumView is the TS rendering view for one EnumSpec.
type tsEnumView struct {
	TypeName    string
	FieldName   string
	UnionValues string // e.g. `'succeeded' | 'rejected'`
}

// tsInterface is the TS rendering view for one DTOSpec.
type tsInterface struct {
	Name   string
	Doc    string
	Fields []tsField
	Enums  []tsEnumView
}

// tsFuncMap is the funcMap for TS templates.
var tsFuncMap = template.FuncMap{
	"not": func(b bool) bool { return !b },
}

// tsTemplates is the parsed TS template set. It is intentionally separate from
// the Go templates var so TS templates never flow through FormatGoSource.
var tsTemplates = template.Must(
	template.New("ts").Funcs(tsFuncMap).ParseFS(tsTemplatesFS,
		"templates/types.ts.tmpl",
		"templates/barrel.ts.tmpl",
	),
)

// tsGoType converts a DTOField to its TypeScript type string.
// It uses the structured fields (ItemDTO, IsList, GoType) to avoid re-parsing
// GoType string for structural decisions.
//
// Mapping:
//   - string           → string
//   - int64/float64    → number
//   - bool/*bool       → boolean
//   - object (ItemDTO!="", !IsList) → ItemDTO name
//   - object[] (ItemDTO!="", IsList) → ItemDTO[]
//   - []string/[]int64/etc → scalar[]
//   - any/[]any        → unknown/unknown[]
//   - enumType (GoType == some enum TypeName) → GoType (the named TS union type)
func tsGoType(f DTOField) string {
	// Object field: ItemDTO is the key signal
	if f.ItemDTO != "" {
		if f.IsList {
			return f.ItemDTO + "[]"
		}
		return f.ItemDTO
	}

	// Check for array prefix first
	if strings.HasPrefix(f.GoType, "[]") {
		elem := f.GoType[2:]
		return tsScalarType(elem) + "[]"
	}

	// Pointer types (e.g. *bool)
	bare := strings.TrimPrefix(f.GoType, "*")
	return tsScalarType(bare)
}

// tsScalarType converts a bare Go scalar type name to TS.
// For unknown types (e.g. enum type names), returns the type name directly
// so the TS union type reference is preserved.
func tsScalarType(goType string) string {
	switch goType {
	case "string":
		return "string"
	case "int64", "float64", "int", "int32", "uint", "uint32", "uint64":
		return "number"
	case "bool":
		return "boolean"
	case "any":
		return "unknown"
	default:
		// Preserve the name as-is (enum type names, other custom types)
		return goType
	}
}

// tsEnumSet builds a set of enum TypeNames for a given DTOSpec, used to look
// up whether a field's GoType is an enum reference.
func tsEnumSet(dto DTOSpec) map[string]bool {
	m := make(map[string]bool, len(dto.Enums))
	for _, e := range dto.Enums {
		m[e.TypeName] = true
	}
	return m
}

// buildTSView converts a ContractGenSpec into the TS rendering view.
// It computes tsInterface entries for each DTO and tsEnumView entries for
// each EnumSpec, so the template only does pure rendering.
func buildTSView(spec *ContractGenSpec) []tsInterface {
	views := make([]tsInterface, 0, len(spec.DTOs))
	for _, dto := range spec.DTOs {
		enumSet := tsEnumSet(dto)

		fields := make([]tsField, 0, len(dto.Fields))
		for _, f := range dto.Fields {
			// Derive wire key: BareJSONTag is set for body fields from schema traversal.
			// Path/query params use paramToField which sets JSONTag but not BareJSONTag,
			// so fall back to stripping ",omitempty" from JSONTag.
			key := f.BareJSONTag
			if key == "" {
				key = strings.TrimSuffix(f.JSONTag, ",omitempty")
			}
			// Skip header fields: json:"-" means the field is header-only and
			// must not appear in the wire-visible TS interface.
			if key == "-" || key == "" {
				continue
			}
			_ = enumSet // enumSet is used implicitly: tsGoType handles enum types via the default case
			fields = append(fields, tsField{
				Key:      key,
				Required: f.Required,
				Type:     tsGoType(f),
			})
		}

		enums := make([]tsEnumView, 0, len(dto.Enums))
		for _, e := range dto.Enums {
			parts := make([]string, 0, len(e.Values))
			for _, v := range e.Values {
				parts = append(parts, "'"+v.Value+"'")
			}
			enums = append(enums, tsEnumView{
				TypeName:    e.TypeName,
				FieldName:   e.FieldName,
				UnionValues: strings.Join(parts, " | "),
			})
		}

		views = append(views, tsInterface{
			Name:   dto.Name,
			Doc:    dto.Doc,
			Fields: fields,
			Enums:  enums,
		})
	}
	return views
}

// tsView is the top-level data passed to types.ts.tmpl.
type tsView struct {
	SourceFile string
	Interfaces []tsInterface
}

// renderTS renders the types.ts file for the given ContractGenSpec.
// It uses text/template directly (no goimports/gofumpt pass) and produces
// output with a "// Code generated by gocell generate contract. DO NOT EDIT."
// header that satisfies governance.IsGoCellGenerated.
func renderTS(spec *ContractGenSpec) ([]byte, error) {
	view := tsView{
		SourceFile: spec.SourceFile,
		Interfaces: buildTSView(spec),
	}
	var buf bytes.Buffer
	if err := tsTemplates.ExecuteTemplate(&buf, "types.ts.tmpl", view); err != nil {
		return nil, fmt.Errorf("renderTS %s: %w", spec.ContractID, err)
	}
	return buf.Bytes(), nil
}

// renderBarrel renders the barrel index.ts file from a list of tsBarrelEntry.
func renderBarrel(entries []tsBarrelEntry) ([]byte, error) {
	var buf bytes.Buffer
	if err := tsTemplates.ExecuteTemplate(&buf, "barrel.ts.tmpl", entries); err != nil {
		return nil, fmt.Errorf("renderBarrel: %w", err)
	}
	return buf.Bytes(), nil
}

// kindEmitsTS reports whether the given spec should produce a types.ts file.
// It returns false for:
//   - responseProjection endpoints (data field rewritten to projection.ResourceProjection)
//   - kinds that don't emit types_gen.go (webhook, grpc)
func kindEmitsTS(spec *ContractGenSpec) bool {
	if spec == nil {
		return false
	}
	// webhook/grpc emit zero contractgen artifacts by design
	if spec.Kind == "webhook" || spec.Kind == "grpc" {
		return false
	}
	// Only kinds that emit types_gen.go get a types.ts
	if !kindHasTypesArtifact(spec.Kind) {
		return false
	}
	// responseProjection: the data field is rewritten to projection.ResourceProjection
	// (not a DTO), TS v1 cannot represent it
	if spec.Endpoint != nil && spec.Endpoint.ResponseProjection {
		return false
	}
	return true
}

// kindHasTypesArtifact reports whether the kind emits types_gen.go by checking
// the contractArtifacts matrix — single source.
func kindHasTypesArtifact(kind string) bool {
	for _, a := range artifactsForKind(kind) {
		if a.template == "types.tmpl" {
			return true
		}
	}
	return false
}

// tsPkgAlias derives a camelCase namespace alias from a generated package path.
// Input: e.g. "generated/contracts/http/order/create/v1"
//
//	or  "generated-ts/contracts/http/order/create/v1"
//
// Output: e.g. "httpOrderCreateV1".
func tsPkgAlias(pkgPath string) string {
	// Strip "generated/contracts/" or "generated-ts/contracts/" prefix if present.
	rel := filepath.ToSlash(pkgPath)
	rel = strings.TrimPrefix(rel, "generated-ts/contracts/")
	rel = strings.TrimPrefix(rel, "generated/contracts/")
	parts := strings.Split(rel, "/")
	if len(parts) == 0 {
		return pkgPath
	}
	var sb strings.Builder
	for i, p := range parts {
		if i == 0 {
			sb.WriteString(p)
			continue
		}
		// Capitalize first letter and handle hyphens
		clean := strings.ReplaceAll(p, "-", "")
		if len(clean) == 0 {
			continue
		}
		sb.WriteString(strings.ToUpper(clean[:1]) + clean[1:])
	}
	return sb.String()
}
