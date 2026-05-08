package cellgen

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

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
	// CellID is the cell identifier (e.g. "foocell").
	CellID string
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
	// DryRun, when true, renders all templates (validating their output) and
	// performs conflict detection but does not write any files to disk.
	DryRun bool
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
// When spec.DryRun is true, templates are rendered (validating output) and
// conflict detection is performed, but no files are written. This allows
// callers to validate inputs and detect path conflicts in CI without mutating
// the filesystem, while still catching template/input errors early.
//
// Generated files:
//   - <root>/<targetDir>/cell.go  — struct + stub markers + initInternal hook
//   - <root>/<targetDir>/cell.yaml — metadata with goStructName set
//
// Implementation note: kept as a single pass (validate → defaults → symlink
// guard → cell.go render → cell.yaml render) so the "all-or-nothing" write
// semantics remain explicit; splitting into phases would force a second-pass
// file walk just to recover state.
//
//nolint:gocognit,cyclop,funlen // see comment above
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

	dir := filepath.Join(root, targetDir)

	cellGoPath := filepath.Join(dir, "cell.go")
	cellYAMLPath := filepath.Join(dir, "cell.yaml")

	// Conflict detection: refuse to overwrite any output file.
	for _, p := range []string{cellGoPath, cellYAMLPath} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("scaffold cell: file already exists: %s", p)
		}
	}

	// DryRun: render templates to catch template/input errors (codegen.FormatGoSource
	// (goimports + gofumpt) validates cell.go syntax), but do not write any files to disk.
	if spec.DryRun {
		if _, err := renderTemplate(cellGoTemplate, spec, true); err != nil {
			return fmt.Errorf("scaffold cell: dry-run render cell.go: %w", err)
		}
		if _, err := renderTemplate(cellYAMLTemplate, spec, false); err != nil {
			return fmt.Errorf("scaffold cell: dry-run render cell.yaml: %w", err)
		}
		return nil
	}

	// Symlink guard: resolve the true on-disk root and verify that the target
	// directory stays inside it even if intermediate components are symlinks.
	// This prevents a pre-placed symlink (e.g. cells/foo → /tmp/evil) from
	// redirecting scaffold writes outside the repository.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("scaffold cell: resolve root %q: %w", root, err)
	}
	cleanTarget := filepath.Clean(filepath.Join(realRoot, targetDir))
	if !strings.HasPrefix(cleanTarget, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("scaffold cell: target %q escapes root %q", targetDir, realRoot)
	}
	// Walk existing parent components and verify each symlink resolves inside root.
	parent := filepath.Dir(cleanTarget)
	for parent != realRoot && parent != "/" && parent != "." {
		info, statErr := os.Lstat(parent)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				parent = filepath.Dir(parent)
				continue
			}
			return fmt.Errorf("scaffold cell: stat parent %q: %w", parent, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, resolveErr := filepath.EvalSymlinks(parent)
			if resolveErr != nil {
				return fmt.Errorf("scaffold cell: resolve symlink %q: %w", parent, resolveErr)
			}
			if !strings.HasPrefix(resolved, realRoot+string(filepath.Separator)) && resolved != realRoot {
				return fmt.Errorf("scaffold cell: parent path %q resolves outside root via symlink (resolved to %q)", parent, resolved)
			}
		}
		parent = filepath.Dir(parent)
	}
	// Use the realRoot-based dir so subsequent writes go to the true path.
	dir = cleanTarget
	cellGoPath = filepath.Join(dir, "cell.go")
	cellYAMLPath = filepath.Join(dir, "cell.yaml")

	cellGoContent, err := renderTemplate(cellGoTemplate, spec, true)
	if err != nil {
		return fmt.Errorf("scaffold cell: render cell.go: %w", err)
	}

	cellYAMLContent, err := renderTemplate(cellYAMLTemplate, spec, false)
	if err != nil {
		return fmt.Errorf("scaffold cell: render cell.yaml: %w", err)
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("scaffold cell: create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(cellGoPath, cellGoContent, 0o600); err != nil {
		return fmt.Errorf("scaffold cell: write cell.go: %w", err)
	}

	if err := os.WriteFile(cellYAMLPath, cellYAMLContent, 0o600); err != nil {
		return fmt.Errorf("scaffold cell: write cell.yaml: %w", err)
	}

	return nil
}

// validateScaffoldSpec returns an error if any required field is missing or
// contains path-traversal sequences. ModulePath is allowed to contain "/"
// (it's a Go module path like "github.com/owner/repo"); other identifier
// fields (CellID/StructName/Package) reject path separators.
//
// Type must be one of validCellTypes (schema-authoritative enum).
// ConsistencyLevel must be one of validConsistencyLevels (schema-authoritative enum).
// Empty Type and ConsistencyLevel are allowed here; defaults are applied by ScaffoldCell.
// OwnerTeam and OwnerRole are required and must match their respective patterns.
//
//nolint:gocognit,cyclop // sequential per-field validation; complexity intrinsic
func validateScaffoldSpec(spec ScaffoldSpec) error {
	idents := []struct {
		name  string
		value string
	}{
		{"CellID", spec.CellID},
		{"StructName", spec.StructName},
		{"Package", spec.Package},
	}
	for _, f := range idents {
		if f.value == "" {
			return fmt.Errorf("scaffold cell: %s is required", f.name)
		}
		if strings.ContainsAny(f.value, `/\`) || strings.Contains(f.value, "..") {
			return fmt.Errorf("scaffold cell: %s contains path traversal or separator", f.name)
		}
	}
	if spec.ModulePath == "" {
		return fmt.Errorf("scaffold cell: ModulePath is required")
	}
	if strings.Contains(spec.ModulePath, "..") || strings.Contains(spec.ModulePath, `\`) {
		return fmt.Errorf("scaffold cell: ModulePath contains path traversal or backslash")
	}
	if len(spec.ModulePath) > 1 && !modulePathPattern.MatchString(spec.ModulePath) {
		return fmt.Errorf("scaffold cell: ModulePath %q is not a valid Go module path", spec.ModulePath)
	}
	if spec.OwnerTeam == "" {
		return fmt.Errorf("scaffold cell: OwnerTeam is required")
	}
	if !ownerTeamPattern.MatchString(spec.OwnerTeam) {
		return fmt.Errorf("scaffold cell: OwnerTeam %q contains invalid characters (allowed: [a-zA-Z0-9_-])", spec.OwnerTeam)
	}
	if spec.OwnerRole == "" {
		return fmt.Errorf("scaffold cell: OwnerRole is required")
	}
	if !ownerRolePattern.MatchString(spec.OwnerRole) {
		return fmt.Errorf("scaffold cell: OwnerRole %q contains invalid characters (allowed: [a-zA-Z0-9_-])", spec.OwnerRole)
	}
	if spec.Type != "" {
		if !containsString(validCellTypes, spec.Type) {
			return fmt.Errorf("scaffold cell: --type must be one of %v, got %q", validCellTypes, spec.Type)
		}
	}
	if spec.ConsistencyLevel != "" {
		if !containsString(validConsistencyLevels, spec.ConsistencyLevel) {
			return fmt.Errorf("scaffold cell: --level must be one of %v, got %q", validConsistencyLevels, spec.ConsistencyLevel)
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
			return nil, fmt.Errorf("rendered Go source is not valid: %w", err)
		}
		return formatted, nil
	}
	return buf.Bytes(), nil
}
