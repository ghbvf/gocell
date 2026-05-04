package cellgen

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// ownerTeamPattern is the whitelist regex for OwnerTeam values written into
// cell.yaml owner.team. Restricts to alphanumerics, hyphens, and underscores
// to prevent YAML injection via newline, colon-space, braces, or path
// traversal sequences.
var ownerTeamPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

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
	// Written as-is to cell.yaml owner.team.
	OwnerTeam string
	// Type is the cell type (e.g. "core", "edge", "support").
	// Defaults to "core" when empty.
	Type string
	// ConsistencyLevel is the cell consistency level (e.g. "L0"-"L4").
	// Defaults to "L1" when empty.
	ConsistencyLevel string
	// DryRun, when true, performs conflict detection and returns the list
	// of paths that would be written without writing any files.
	DryRun bool
}

// cellGoTemplate is parsed once from the shared templateFS. Uses
// template.Must per PANIC-REGISTERED-01 — standard library Must pattern
// is the registered panic exception for embedded asset failures (the
// embed.FS is fixed at compile time; failure here is a build-system bug).
var cellGoTemplate = template.Must(template.New("scaffold-cell.tmpl").ParseFS(templateFS, "templates/scaffold-cell.tmpl"))

// cellYAMLTemplate renders the cell.yaml skeleton with goStructName pre-set.
// OwnerTeam is written to owner.team; role is left as TODO for the developer.
// Type and ConsistencyLevel are rendered from the spec (defaults: "core" / "L1").
var cellYAMLTemplate = template.Must(template.New("cell-yaml").Parse(`id: {{.CellID}}
type: {{.Type}}
consistencyLevel: {{.ConsistencyLevel}}
durabilityMode: durable
owner:
  team: {{.OwnerTeam}}
  role: TODO
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
// When spec.DryRun is true, only conflict detection is performed — no files
// are written. Callers can use this to validate inputs and detect path
// conflicts in CI without mutating the filesystem.
//
// Generated files:
//   - <root>/<targetDir>/cell.go  — struct + stub markers + initInternal hook
//   - <root>/<targetDir>/cell.yaml — metadata with goStructName set
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

	// DryRun: conflict check passed; skip filesystem mutations.
	if spec.DryRun {
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
	if spec.OwnerTeam != "" && !ownerTeamPattern.MatchString(spec.OwnerTeam) {
		return fmt.Errorf("scaffold cell: OwnerTeam %q contains invalid characters (allowed: [a-zA-Z0-9_-])", spec.OwnerTeam)
	}
	return nil
}

// renderTemplate executes tmpl with data and returns the rendered bytes.
// When isGoSource is true the output is validated (and formatted) via
// go/format.Source so that template bugs are caught at scaffold time rather
// than producing invalid Go that breaks at compile time.
func renderTemplate(tmpl *template.Template, data any, isGoSource bool) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	if isGoSource {
		formatted, err := format.Source(buf.Bytes())
		if err != nil {
			return nil, fmt.Errorf("rendered Go source is not valid: %w", err)
		}
		return formatted, nil
	}
	return buf.Bytes(), nil
}
