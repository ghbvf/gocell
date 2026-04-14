package scaffold

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Error codes specific to the scaffold package.
const (
	ErrScaffoldConflict    errcode.Code = "ERR_SCAFFOLD_CONFLICT"
	ErrScaffoldInvalidOpts errcode.Code = "ERR_SCAFFOLD_INVALID_OPTS"
	ErrScaffoldCellMissing errcode.Code = "ERR_SCAFFOLD_CELL_MISSING"
	ErrScaffoldTemplate    errcode.Code = "ERR_SCAFFOLD_TEMPLATE"
	ErrScaffoldIO          errcode.Code = "ERR_SCAFFOLD_IO"
)

// validatePathComponent rejects identifiers that contain path traversal
// sequences or separators, preventing writes outside the project root.
func validatePathComponent(value, field string) error {
	if value == "" {
		return errcode.New(ErrScaffoldInvalidOpts, field+" is required")
	}
	if value == "." || strings.Contains(value, "..") || strings.ContainsAny(value, `/\`) {
		return errcode.New(ErrScaffoldInvalidOpts, field+" contains path traversal or separator")
	}
	return nil
}

// CellOpts defines options for scaffolding a new cell.
type CellOpts struct {
	ID               string
	Type             string // "core" (default)
	ConsistencyLevel string // "L2" (default)
	OwnerTeam        string
}

// SliceOpts defines options for scaffolding a new slice.
type SliceOpts struct {
	ID     string
	CellID string
}

// ContractOpts defines options for scaffolding a new contract.
type ContractOpts struct {
	ID        string // e.g., "event.session.revoked.v1"
	Kind      string // http|event|command|projection
	OwnerCell string
}

// JourneyOpts defines options for scaffolding a new journey.
type JourneyOpts struct {
	ID        string
	Goal      string
	OwnerTeam string
	Cells     []string
}

// Scaffolder generates directory structures and YAML templates.
type Scaffolder struct {
	root string // project root containing go.mod
}

// New creates a Scaffolder rooted at the given directory.
func New(root string) *Scaffolder {
	return &Scaffolder{root: root}
}

// CreateCell creates cells/{id}/cell.yaml with directory.
// Returns an error if the cell directory already exists (skip-on-conflict).
func (s *Scaffolder) CreateCell(opts CellOpts) error {
	if err := validatePathComponent(opts.ID, "cell ID"); err != nil {
		return err
	}
	if opts.OwnerTeam == "" {
		return errcode.New(ErrScaffoldInvalidOpts, "cell owner team is required")
	}

	// Apply defaults.
	if opts.Type == "" {
		opts.Type = "core"
	}
	if opts.ConsistencyLevel == "" {
		opts.ConsistencyLevel = "L2"
	}

	dir := filepath.Join(s.root, "cells", opts.ID)
	outPath := filepath.Join(dir, "cell.yaml")

	return s.renderToFile("templates/cell.yaml.tpl", outPath, opts)
}

// CreateSlice creates cells/{cellID}/slices/{id}/slice.yaml.
// Returns an error if the cell doesn't exist or the slice directory already exists.
func (s *Scaffolder) CreateSlice(opts SliceOpts) error {
	if err := validatePathComponent(opts.ID, "slice ID"); err != nil {
		return err
	}
	if err := validatePathComponent(opts.CellID, "slice cell ID"); err != nil {
		return err
	}

	// Verify the parent cell exists.
	cellDir := filepath.Join(s.root, "cells", opts.CellID)
	if _, err := os.Stat(cellDir); os.IsNotExist(err) {
		return errcode.New(ErrScaffoldCellMissing,
			fmt.Sprintf("cell %q does not exist, create it first", opts.CellID))
	}

	dir := filepath.Join(cellDir, "slices", opts.ID)
	outPath := filepath.Join(dir, "slice.yaml")

	return s.renderToFile("templates/slice.yaml.tpl", outPath, opts)
}

// CreateContract creates contracts/{kind}/{domain...}/{version}/contract.yaml.
// Contract ID format: "{kind}.{domain}.{operation}.{version}"
// Directory: contracts/{kind}/{domain}/{operation}/{version}/
func (s *Scaffolder) CreateContract(opts ContractOpts) error {
	if opts.ID == "" {
		return errcode.New(ErrScaffoldInvalidOpts, "contract ID is required")
	}
	if err := validatePathComponent(opts.Kind, "contract kind"); err != nil {
		return err
	}
	if err := validatePathComponent(opts.OwnerCell, "contract owner cell"); err != nil {
		return err
	}
	// Validate each dot-segment of the ID for path traversal.
	for _, seg := range strings.Split(opts.ID, ".") {
		if err := validatePathComponent(seg, "contract ID segment"); err != nil {
			return err
		}
	}

	validKinds := map[string]bool{"http": true, "event": true, "command": true, "projection": true}
	if !validKinds[opts.Kind] {
		return errcode.New(ErrScaffoldInvalidOpts,
			fmt.Sprintf("invalid contract kind %q, must be one of: http, event, command, projection", opts.Kind))
	}

	// Parse ID to directory path: "event.session.revoked.v1" → contracts/event/session/revoked/v1/
	parts := strings.Split(opts.ID, ".")
	if len(parts) < 3 {
		return errcode.New(ErrScaffoldInvalidOpts,
			fmt.Sprintf("contract ID %q must have at least 3 dot-separated segments (kind.domain.version)", opts.ID))
	}
	if parts[0] != opts.Kind {
		return errcode.New(ErrScaffoldInvalidOpts,
			fmt.Sprintf("contract ID prefix %q must match kind %q", parts[0], opts.Kind))
	}

	// Build path from all parts.
	pathParts := append([]string{s.root, "contracts"}, parts...)
	dir := filepath.Join(pathParts...)
	outPath := filepath.Join(dir, "contract.yaml")

	tplName := fmt.Sprintf("templates/contract-%s.yaml.tpl", opts.Kind)

	return s.renderToFile(tplName, outPath, opts)
}

// CreateJourney creates journeys/J-{name}.yaml (or journeys/{id}.yaml if id starts with "J-").
func (s *Scaffolder) CreateJourney(opts JourneyOpts) error {
	if err := validatePathComponent(opts.ID, "journey ID"); err != nil {
		return err
	}
	if opts.Goal == "" {
		return errcode.New(ErrScaffoldInvalidOpts, "journey goal is required")
	}
	if opts.OwnerTeam == "" {
		return errcode.New(ErrScaffoldInvalidOpts, "journey owner team is required")
	}
	if len(opts.Cells) == 0 {
		return errcode.New(ErrScaffoldInvalidOpts, "journey must reference at least one cell")
	}

	// Normalize: ensure ID carries the J- prefix for both filename and template.
	if !strings.HasPrefix(opts.ID, "J-") {
		opts.ID = "J-" + opts.ID
	}
	filename := opts.ID + ".yaml"

	dir := filepath.Join(s.root, "journeys")
	outPath := filepath.Join(dir, filename)

	return s.renderToFile("templates/journey.yaml.tpl", outPath, opts)
}

// funcMap provides template helper functions.
var funcMap = template.FuncMap{
	// replace takes (old, new, s) so the piped value arrives as the last arg.
	// Usage: {{.ID | replace "-" "_"}}
	"replace": func(old, new, s string) string {
		return strings.ReplaceAll(s, old, new)
	},
}

// renderToFile loads a template from the embedded FS, renders it with the given
// data, and writes the result to outPath. It creates parent directories as
// needed and returns ErrScaffoldConflict if the file already exists.
func (s *Scaffolder) renderToFile(tplPath, outPath string, data any) error {
	// Skip-on-conflict: refuse to overwrite.
	if _, err := os.Stat(outPath); err == nil {
		return errcode.New(ErrScaffoldConflict,
			fmt.Sprintf("file already exists: %s", outPath))
	}

	// Load and parse template.
	raw, err := templateFS.ReadFile(tplPath)
	if err != nil {
		return errcode.Wrap(ErrScaffoldTemplate,
			fmt.Sprintf("scaffold: failed to read template %s", tplPath), err)
	}

	tmpl, err := template.New(filepath.Base(tplPath)).Funcs(funcMap).Parse(string(raw))
	if err != nil {
		return errcode.Wrap(ErrScaffoldTemplate,
			fmt.Sprintf("scaffold: failed to parse template %s", tplPath), err)
	}

	// Render.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return errcode.Wrap(ErrScaffoldTemplate,
			fmt.Sprintf("scaffold: failed to execute template %s", tplPath), err)
	}

	// Create directories and write file.
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errcode.Wrap(ErrScaffoldIO,
			fmt.Sprintf("scaffold: failed to create directory %s", dir), err)
	}

	if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
		return errcode.Wrap(ErrScaffoldIO,
			fmt.Sprintf("scaffold: failed to write file %s", outPath), err)
	}

	return nil
}
