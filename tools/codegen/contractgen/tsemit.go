package contractgen

import (
	"bytes"
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
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

// tsQuote wraps a string value in TypeScript single-quoted literal syntax,
// escaping backslashes and single-quotes so the result is valid TS source.
// Minimal escaping: only '\' → '\\' and '\” → '\” are needed for a
// single-quoted string literal (double-quotes are harmless inside single quotes).
func tsQuote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return "'" + s + "'"
}

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

// buildTSView converts a ContractGenSpec into the TS rendering view.
// It computes tsInterface entries for each DTO and tsEnumView entries for
// each EnumSpec, so the template only does pure rendering.
func buildTSView(spec *ContractGenSpec) []tsInterface {
	views := make([]tsInterface, 0, len(spec.DTOs))
	for _, dto := range spec.DTOs {
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
			// tsGoType handles enum type names via the default case (preserves the
			// named type as-is so the TS union type reference is intact).
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
				parts = append(parts, tsQuote(v.Value))
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
//
// Hyphens within a path segment are treated as word boundaries: each sub-word
// after a hyphen gets its first letter capitalised before joining. The first
// path segment (e.g. "event", "http") keeps its natural lower-case start so the
// alias begins with a lower-case letter (namespace convention). Subsequent
// segments — including any sub-words created by hyphens — are title-cased.
//
// Examples:
//
//	"generated/contracts/http/order/create/v1"              → "httpOrderCreateV1"
//	"generated-ts/contracts/event/auth/bootstrap-failed/v1" → "eventAuthBootstrapFailedV1"
//	"generated/contracts/event/devicecert-rotation-resolved/v1" → "eventDevicecertRotationResolvedV1"
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
		// Split on hyphens to handle multi-word segments like "bootstrap-failed".
		subWords := strings.Split(p, "-")
		for j, w := range subWords {
			if len(w) == 0 {
				continue
			}
			// First sub-word of the first segment: keep original case (lower-case start).
			if i == 0 && j == 0 {
				sb.WriteString(w)
				continue
			}
			// All other sub-words: title-case the first letter.
			sb.WriteString(strings.ToUpper(w[:1]) + w[1:])
		}
	}
	return sb.String()
}

// tsTypesPath returns the absolute path for a contract's types.ts file under
// generated-ts/. The PackagePath "generated/contracts/{kind}/{path}/{version}"
// maps to "generated-ts/contracts/{kind}/{path}/{version}/types.ts".
func tsTypesPath(root string, spec *ContractGenSpec) string {
	rel := filepath.ToSlash(spec.PackagePath)
	tsRel := strings.TrimPrefix(rel, "generated/")
	return filepath.Join(root, "generated-ts", filepath.FromSlash(tsRel), "types.ts")
}

// emitTSTypes renders and writes (or dry-runs / verifies) the per-contract
// types.ts file for the given spec. It uses renderTS (text/template, no
// goimports/gofumpt) and codegen.Write directly — TS must NOT go through
// codegen.Render.
func emitTSTypes(root string, spec *ContractGenSpec, opts Options, res *Result) error {
	content, err := renderTS(spec)
	if err != nil {
		return err
	}
	writeRes, err := codegen.Write(codegen.WriteOptions{
		Path:     tsTypesPath(root, spec),
		Content:  content,
		RepoRoot: root,
		DryRun:   opts.DryRun,
		Verify:   opts.Verify,
	})
	if err != nil {
		return err
	}
	recordContractResult(res, writeRes)
	return nil
}

// tsBarrelEntryFor derives the barrel entry (namespace alias + relative import
// path) for a TS-emitting contract spec. Shared by RenderTSBarrel so the disk
// and generatedverify manifest barrels are byte-identical.
func tsBarrelEntryFor(root string, spec *ContractGenSpec) (tsBarrelEntry, error) {
	tsRel, err := relFromRoot(root, tsTypesPath(root, spec))
	if err != nil {
		return tsBarrelEntry{}, err
	}
	importRel := strings.TrimPrefix(tsRel, "generated-ts/")
	return tsBarrelEntry{
		Alias:      tsPkgAlias(strings.TrimSuffix(tsRel, "/types.ts")),
		ImportPath: "./" + strings.TrimSuffix(importRel, ".ts"),
	}, nil
}

// RenderTSBarrel renders the generated-ts/index.ts barrel from EVERY codegen
// contract that emits a types.ts. It scans the full project (NOT a generate
// scope) so the barrel is identical whether one contract or all are generated,
// and both the disk generator (Generate) and the generatedverify manifest
// derive it from this single source. The bool result is false when no contract
// emits TS (e.g. a project of only webhook/grpc/responseProjection contracts,
// or the synthetic generatedverify fixtures) — in that case no barrel is written.
func RenderTSBarrel(root string, p *metadata.ProjectMeta) (CodegenArtifact, bool, error) {
	if p == nil {
		return CodegenArtifact{}, false, fmt.Errorf(errPrefixRender + "project is nil")
	}
	ids := make([]string, 0, len(p.Contracts))
	for id, c := range p.Contracts {
		if c.Codegen {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	var entries []tsBarrelEntry
	for _, id := range ids {
		spec, err := buildContractSpec(root, p, id)
		if err != nil {
			return CodegenArtifact{}, false, fmt.Errorf(errPrefixRender+"barrel %q: %w", id, err)
		}
		if !kindEmitsTS(spec) {
			continue
		}
		entry, err := tsBarrelEntryFor(root, spec)
		if err != nil {
			return CodegenArtifact{}, false, err
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return CodegenArtifact{}, false, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Alias < entries[j].Alias })

	content, err := renderBarrel(entries)
	if err != nil {
		return CodegenArtifact{}, false, err
	}
	rel, err := relFromRoot(root, filepath.Join(root, "generated-ts", "index.ts"))
	if err != nil {
		return CodegenArtifact{}, false, err
	}
	return CodegenArtifact{Path: rel, Content: content}, true, nil
}

// generateTSBarrel renders the full-project barrel and writes (or dry-runs /
// verifies) generated-ts/index.ts. No-op when no contract emits TS.
func generateTSBarrel(root string, p *metadata.ProjectMeta, opts Options, res *Result) error {
	barrel, ok, err := RenderTSBarrel(root, p)
	if err != nil {
		return fmt.Errorf(errPrefixGenerate+"barrel index.ts: %w", err)
	}
	if !ok {
		return nil
	}
	writeRes, err := codegen.Write(codegen.WriteOptions{
		Path:     filepath.Join(root, filepath.FromSlash(barrel.Path)),
		Content:  barrel.Content,
		RepoRoot: root,
		DryRun:   opts.DryRun,
		Verify:   opts.Verify,
	})
	if err != nil {
		return err
	}
	recordContractResult(res, writeRes)
	return nil
}

// appendTSArtifact appends the per-contract types.ts CodegenArtifact to out when
// the spec emits TS (skipped for webhook/grpc/responseProjection via
// kindEmitsTS), returning out unchanged otherwise. Byte-identical to the disk
// write — both derive from renderTS(spec) + tsTypesPath.
func appendTSArtifact(out []CodegenArtifact, root string, spec *ContractGenSpec) ([]CodegenArtifact, error) {
	if !kindEmitsTS(spec) {
		return out, nil
	}
	content, err := renderTS(spec)
	if err != nil {
		return nil, fmt.Errorf(errPrefixRender+"%q types.ts: %w", spec.ContractID, err)
	}
	rel, err := relFromRoot(root, tsTypesPath(root, spec))
	if err != nil {
		return nil, err
	}
	return append(out, CodegenArtifact{Path: rel, Content: content}), nil
}
