package cellgen

import (
	"bytes"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/ghbvf/gocell/kernel/scaffoldid"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/tools/codegen"
)

// ownerTeamPattern is the whitelist regex for OwnerTeam values written into
// cell.yaml owner.team. Restricts to alphanumerics, hyphens, and underscores
// to prevent YAML injection via newline, colon-space, braces, or path
// traversal sequences.
var ownerTeamPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ownerRolePattern is the whitelist regex for OwnerRole values written into
// cell.yaml owner.role. Same character class as ownerTeamPattern.
var ownerRolePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// modulePathPattern validates Go module paths (e.g. "github.com/owner/repo").
// Allows letters, digits, hyphens, underscores, dots, and forward slashes.
// Prohibits backslash, "..", and leading/trailing slashes.
var modulePathPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._\-/]*[a-zA-Z0-9]$`)

// validCellTypes is the authoritative list of cell type values, derived from
// kernel/metadata/schemas/cell.schema.json "type" enum.
var validCellTypes = []string{"core", "edge", "support"}

// validConsistencyLevels is the authoritative list of consistency level values,
// derived from kernel/metadata/schemas/cell.schema.json "consistencyLevel" enum.
var validConsistencyLevels = []string{"L0", "L1", "L2", "L3", "L4"}

// ScaffoldSpec holds the inputs required to render a new cell skeleton.
type ScaffoldSpec struct {
	// CellID is the cell identifier (e.g. "foocell"). Typed so the
	// (^[a-z][a-z0-9]+$) constraint is established at construction time via
	// scaffoldid.Parse — callers cannot supply an unvalidated raw string
	// (SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01).
	CellID scaffoldid.ScaffoldID
	// StructName is the Go struct name (e.g. "FooCell").
	StructName string
	// Package is the Go package name (e.g. "foocell").
	Package string
	// ModulePath is the Go module path (e.g. "github.com/ghbvf/gocell").
	ModulePath string
	// OwnerTeam is the team responsible for this cell (e.g. "platform").
	// Required. Written as-is to cell.yaml owner.team.
	OwnerTeam string
	// OwnerRole is the ownership role for this cell (e.g. "cell-owner").
	// Required. Written as-is to cell.yaml owner.role.
	OwnerRole string
	// Type is the cell type. Must be one of validCellTypes.
	// Defaults to "core" when empty.
	Type string
	// ConsistencyLevel is the cell consistency level. Must be one of validConsistencyLevels.
	// Defaults to "L1" when empty.
	ConsistencyLevel string
	// SkipGenerate, when true, skips the appended cellgen/contractgen derived
	// artifacts from PlanCellBundleScaffold. The resulting plan contains only
	// skeleton files. Mirrors assembly.AssemblyScaffoldSpec.SkipGenerate semantics.
	SkipGenerate bool
	// WithHTTP, WithEvents, WithBoth control K#09 ScaffoldCellBundle's contract
	// variant. WithHTTP produces an HTTP request/response contract (default
	// when none of the three flags are set). WithEvents produces an event
	// payload/headers contract. WithBoth produces both.
	WithHTTP   bool
	WithEvents bool
	WithBoth   bool
}

// cellGoTemplate is parsed once from the shared templateFS. Uses
// template.Must per PANIC-REGISTERED-01 — standard library Must pattern
// is the registered panic exception for embedded asset failures (the
// embed.FS is fixed at compile time; failure here is a build-system bug).
var cellGoTemplate = template.Must(template.New("scaffold-cell.tmpl").ParseFS(templateFS, "templates/scaffold-cell.tmpl"))

// cellYAMLTemplate renders the cell.yaml skeleton with goStructName pre-set.
// OwnerTeam and OwnerRole are written from the spec (both required by CLI).
// Type and ConsistencyLevel are rendered from the spec (defaults: "core" / "L1").
var cellYAMLTemplate = template.Must(template.New("cell-yaml").Parse(`id: {{.CellID}}
type: {{.Type}}
consistencyLevel: {{.ConsistencyLevel}}
durabilityMode: durable
owner:
  team: {{.OwnerTeam}}
  role: {{.OwnerRole}}
schema:
  primary: {{.CellID}}
verify:
  smoke:
    - smoke.{{.CellID}}.startup
goStructName: {{.StructName}}
l0Dependencies: []
`))

// ScaffoldCell renders a new cell skeleton at root/<targetDir> with stub
// markers, cell.yaml, and the K#05 marker conventions in place. Returns an
// error if any output file already exists (caller must remove first).
//
// Generated files:
//   - <root>/<targetDir>/cell.go  — struct + stub markers + initInternal hook
//   - <root>/<targetDir>/cell.yaml — metadata with goStructName set
//
// All filesystem writes go through pathsafe.WritePlannedFiles (SCAFFOLD-WRITE-FUNNEL-01).
func ScaffoldCell(root, targetDir string, spec ScaffoldSpec) error {
	if err := validateScaffoldSpec(spec); err != nil {
		return err
	}

	// Apply defaults for optional fields.
	if spec.Type == "" {
		spec.Type = "core"
	}
	if spec.ConsistencyLevel == "" {
		spec.ConsistencyLevel = "L1"
	}

	// Always render templates to catch template/input errors early (even on dry-run).
	// cellTemplateData embeds spec and adds ListenerMarker so the template can
	// reference {{.ListenerMarker}} (SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01).
	cellGoContent, err := renderTemplate(cellGoTemplate, cellTemplateData{
		ScaffoldSpec:   spec,
		ListenerMarker: ListenerMarker,
	}, true)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "scaffold cell: render cell.go failed", err)
	}
	cellYAMLContent, err := renderTemplate(cellYAMLTemplate, spec, false)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "scaffold cell: render cell.yaml failed", err)
	}

	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "scaffold cell: resolve root", err)
	}

	absDir, err := pathsafe.ContainPath(realRoot, targetDir)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "scaffold cell: contain path", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(absDir, "cell.go"), Content: cellGoContent},
		{AbsPath: filepath.Join(absDir, "cell.yaml"), Content: cellYAMLContent},
	}

	// Return WritePlannedFiles error directly: pathsafe already returns a
	// structured *errcode.Error (ErrConflict for file-exists, ErrInternal for
	// OS errors) so re-wrapping would clobber the Code and lose the caller's
	// ability to errors.As to ErrConflict.
	ps, err := pathsafe.NewPlanSet(plan)
	if err != nil {
		return err
	}
	return pathsafe.WritePlannedFiles(realRoot, ps, false)
}

// validateScaffoldSpec returns an error if any required field is missing or
// contains path-traversal sequences. ModulePath is allowed to contain "/"
// (it's a Go module path like "github.com/owner/repo"); other identifier
// fields (CellID/StructName/Package) reject path separators.
//
// CellID must not contain '-' (kebab-case): use a no-dash identifier.
// Type must be one of validCellTypes (schema-authoritative enum).
// ConsistencyLevel must be one of validConsistencyLevels (schema-authoritative enum).
// Empty Type and ConsistencyLevel are allowed here; defaults are applied by ScaffoldCell.
// OwnerTeam and OwnerRole are required and must match their respective patterns.
func validateScaffoldSpec(spec ScaffoldSpec) error {
	if err := validateIdentifierFields(spec); err != nil {
		return err
	}
	if err := validateModulePath(spec); err != nil {
		return err
	}
	if err := validateOwnerFields(spec); err != nil {
		return err
	}
	return validateEnumFields(spec)
}

// validateIdentifierFields validates StructName and Package fields.
// CellID is now typed (scaffoldid.ScaffoldID) and validated at construction
// via scaffoldid.Parse; the path-traversal / no-dash checks are subsumed by
// the AssemblyIDPattern (`^[a-z][a-z0-9]+$`) the Parse funnel enforces.
func validateIdentifierFields(spec ScaffoldSpec) error {
	if spec.CellID.IsZero() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold cell: required field missing",
			errcode.WithDetails(slog.String("field", "CellID")))
	}
	idents := []struct {
		name  string
		value string
	}{
		{"StructName", spec.StructName},
		{"Package", spec.Package},
	}
	for _, f := range idents {
		if f.value == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"scaffold cell: required field missing",
				errcode.WithDetails(slog.String("field", f.name)))
		}
		if strings.ContainsAny(f.value, `/\`) || strings.Contains(f.value, "..") {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"scaffold cell: field contains path traversal or separator",
				errcode.WithDetails(slog.String("field", f.name)))
		}
	}
	return nil
}

// validateModulePath validates the ModulePath field.
func validateModulePath(spec ScaffoldSpec) error {
	if spec.ModulePath == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold cell: ModulePath is required")
	}
	if strings.Contains(spec.ModulePath, "..") || strings.Contains(spec.ModulePath, `\`) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold cell: ModulePath contains path traversal or backslash")
	}
	if len(spec.ModulePath) > 1 && !modulePathPattern.MatchString(spec.ModulePath) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold cell: ModulePath is not a valid Go module path",
			errcode.WithDetails(slog.String("modulePath", spec.ModulePath)))
	}
	return nil
}

// validateOwnerFields validates OwnerTeam and OwnerRole fields.
func validateOwnerFields(spec ScaffoldSpec) error {
	if spec.OwnerTeam == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold cell: OwnerTeam is required")
	}
	if !ownerTeamPattern.MatchString(spec.OwnerTeam) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold cell: OwnerTeam contains invalid characters (allowed: [a-zA-Z0-9_-])",
			errcode.WithDetails(slog.String("ownerTeam", spec.OwnerTeam)))
	}
	if spec.OwnerRole == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold cell: OwnerRole is required")
	}
	if !ownerRolePattern.MatchString(spec.OwnerRole) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold cell: OwnerRole contains invalid characters (allowed: [a-zA-Z0-9_-])",
			errcode.WithDetails(slog.String("ownerRole", spec.OwnerRole)))
	}
	return nil
}

// validateEnumFields validates Type and ConsistencyLevel enum fields.
func validateEnumFields(spec ScaffoldSpec) error {
	if spec.Type != "" {
		if !containsString(validCellTypes, spec.Type) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"scaffold cell: --type must be one of validCellTypes",
				errcode.WithDetails(
					slog.String("type", spec.Type),
					slog.String("allowed", fmt.Sprintf("%v", validCellTypes)),
				))
		}
	}
	if spec.ConsistencyLevel != "" {
		if !containsString(validConsistencyLevels, spec.ConsistencyLevel) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"scaffold cell: --level must be one of validConsistencyLevels",
				errcode.WithDetails(
					slog.String("level", spec.ConsistencyLevel),
					slog.String("allowed", fmt.Sprintf("%v", validConsistencyLevels)),
				))
		}
	}
	return nil
}

// containsString reports whether target exists in the slice.
func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

// renderTemplate executes tmpl with data and returns the rendered bytes.
// When isGoSource is true the output is routed through codegen.FormatGoSource
// (goimports → gofumpt) so scaffolded files match the CI formatter gate
// (.golangci.yml gofumpt) and template bugs surface at scaffold time rather
// than producing invalid Go that breaks at compile time.
func renderTemplate(tmpl *template.Template, data any, isGoSource bool) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	if isGoSource {
		formatted, err := codegen.FormatGoSource("", buf.Bytes())
		if err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "rendered Go source is not valid", err)
		}
		return formatted, nil
	}
	return buf.Bytes(), nil
}
