// Package archtest hosts K#05 unified codegen invariant gates.
//
// These gates protect the K#05 wire+metadata single-source contract on
// cells that have opted into K#04 codegen. After K#05:
//
//   - cell.go MUST NOT contain `&metadata.CellMeta{...}` literals — the
//     metadata literal is owned by cell_gen.go's `loadCellMetadata()`
//     (NO-METADATA-LITERAL-IN-CELLGO-01).
//   - cell.yaml MUST NOT contain `listeners:`; slice.yaml MUST NOT
//     contain `routeMounts:` or `subscribes:` — wire is single-sourced
//     in cell.go marker comments (NO-WIRE-FIELDS-IN-YAML-01).
//   - cell.go / cell_init.go MUST NOT call `reg.RouteGroup(` or
//     `reg.Subscribe(` — wire is owned by cell_gen.go's generated Init
//     after K#04 opt-in (MARKER-MISSING-FOR-WIRE-CALL-01).
//   - For each cell with markers, markergen.Merge MUST succeed
//     (MARKERGEN-DRIFT-VERIFY-01).
//   - For each opted-in cell with reg.RouteGroup in cell_gen.go, the
//     cell.go MUST declare at least one `// +cell:listener` marker
//     (MARKER-WIRE-SINGLE-SOURCE-01).
//
// ref: kubernetes-sigs/controller-tools pkg/markers/parse.go@main
//
//	(splitMarker formal grammar)
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#05
package archtest

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// Compile-time signature lock for markergen.Merge — the K#05 contract is
// `func(string, *metadata.ProjectMeta) (map[string]markergen.WireBundle, error)`.
// If this fails to compile after W2 implementation, the public surface has
// drifted from the design.
var _ func(string, *metadata.ProjectMeta) (map[string]markergen.WireBundle, error) = markergen.Merge

// findAllCellFiles walks both cells/ and examples/*/cells/ collecting
// production cell.go files (cells/<name>/cell.go and
// examples/<project>/cells/<name>/cell.go). Excludes _test.go, vendor,
// worktrees, testdata, slices, and internal subdirectories.
func findAllCellFiles(root string) []string {
	var files []string
	roots := []string{
		filepath.Join(root, "cells"),
		filepath.Join(root, "examples"),
	}
	for _, scanRoot := range roots {
		_ = filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // walk continues past unreadable entries by design
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", "worktrees", "testdata", ".git", "slices", "internal", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == "cell.go" {
				files = append(files, path)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}

// findAllCellInitFiles finds cells/**/cell.go, cells/**/cell_init.go, and
// the same under examples/*/cells/. Used by MARKER-MISSING-FOR-WIRE-CALL-01
// to scan for forbidden reg.RouteGroup / reg.Subscribe calls.
func findAllCellInitFiles(root string) []string {
	var files []string
	roots := []string{
		filepath.Join(root, "cells"),
		filepath.Join(root, "examples"),
	}
	for _, scanRoot := range roots {
		_ = filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // walk continues past unreadable entries by design
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", "worktrees", "testdata", ".git", "slices", "internal", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			if name == "cell_gen.go" {
				return nil // generated owns wire calls
			}
			// Match cell.go, cell_init.go, cell_routes.go, cell_providers.go.
			if name == "cell.go" || strings.HasPrefix(name, "cell_") {
				files = append(files, path)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}

// findAllCellYAMLs walks cells/**/cell.yaml and examples/*/cells/**/cell.yaml.
func findAllCellYAMLs(root string) []string {
	var files []string
	roots := []string{
		filepath.Join(root, "cells"),
		filepath.Join(root, "examples"),
	}
	for _, scanRoot := range roots {
		_ = filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // best-effort walk
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", "worktrees", "testdata", ".git", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == "cell.yaml" {
				files = append(files, path)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}

// findAllSliceYAMLs walks cells/**/slices/*/slice.yaml and the examples
// equivalent.
func findAllSliceYAMLs(root string) []string {
	var files []string
	roots := []string{
		filepath.Join(root, "cells"),
		filepath.Join(root, "examples"),
	}
	for _, scanRoot := range roots {
		_ = filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // best-effort walk
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", "worktrees", "testdata", ".git", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == "slice.yaml" {
				files = append(files, path)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}

// deriveCellID extracts the cell ID from a cell file path. Examples:
//
//	cells/configcore/cell.go              -> "configcore"
//	examples/iotdevice/cells/devicecell/cell.go -> "devicecell"
func deriveCellID(path string) string {
	dir := filepath.Dir(path)
	return filepath.Base(dir)
}

// TestNoMetadataLiteralInCellGo01 verifies NO-METADATA-LITERAL-IN-CELLGO-01.
// cell.go MUST NOT declare `&metadata.CellMeta{...}` composite literals — after
// K#05, metadata is single-sourced via cell_gen.go's `loadCellMetadata()` which
// returns `var cellMeta = &metadata.CellMeta{...}` rendered from cell.yaml.
func TestNoMetadataLiteralInCellGo01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	for _, path := range findAllCellFiles(root) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || lit.Type == nil {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkgIdent.Name == "metadata" && sel.Sel.Name == "CellMeta" {
				rel, _ := filepath.Rel(root, path)
				rel = filepath.ToSlash(rel)
				t.Errorf("NO-METADATA-LITERAL-IN-CELLGO-01: %s:%d declares &metadata.CellMeta{...} literal — "+
					"metadata is owned by cell_gen.go's loadCellMetadata() after K#05; run `gocell generate cell %s`",
					rel, fset.Position(lit.Pos()).Line, deriveCellID(path))
			}
			return true
		})
	}
}

// TestNoWireFieldsInYaml01 verifies NO-WIRE-FIELDS-IN-YAML-01.
// cell.yaml MUST NOT contain `listeners:`; slice.yaml MUST NOT contain
// `routeMounts:` or `subscribes:` — wire is single-sourced in cell.go
// marker comments after K#05.
func TestNoWireFieldsInYaml01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	for _, path := range findAllCellYAMLs(root) {
		content, err := os.ReadFile(path) //nolint:gosec // archtest scans repo paths it discovered
		if err != nil {
			continue
		}
		if hasYAMLTopKey(content, "listeners") {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("NO-WIRE-FIELDS-IN-YAML-01: %s declares `listeners:` — wire is owned by cell.go markers (// +cell:listener); remove the field",
				filepath.ToSlash(rel))
		}
	}
	for _, path := range findAllSliceYAMLs(root) {
		content, err := os.ReadFile(path) //nolint:gosec // archtest scans repo paths it discovered
		if err != nil {
			continue
		}
		for _, key := range []string{"routeMounts", "subscribes"} {
			if hasYAMLTopKey(content, key) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf(
					"NO-WIRE-FIELDS-IN-YAML-01: %s declares `%s:` — wire is owned by cell.go markers "+
						"(// +slice:route / // +slice:subscribe); remove the field",
					filepath.ToSlash(rel), key)
			}
		}
	}
}

// hasYAMLTopKey reports whether content contains a top-level YAML key
// (line begins with `<key>:` with no leading whitespace, ignoring blank
// or comment lines).
func hasYAMLTopKey(content []byte, key string) bool {
	prefix := []byte(key + ":")
	for _, line := range bytes.Split(content, []byte("\n")) {
		if len(line) == 0 || line[0] == '#' || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if bytes.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// TestMarkerMissingForWireCall01 verifies MARKER-MISSING-FOR-WIRE-CALL-01.
// cell.go / cell_init.go / cell_routes.go MUST NOT call `reg.RouteGroup(`
// or `reg.Subscribe(` — these calls are owned by cell_gen.go's generated
// Init after K#04 opt-in. New routes/subscribes are declared via marker
// comments and rendered into cell_gen.go.
func TestMarkerMissingForWireCall01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	forbidden := [][]byte{
		[]byte("reg.RouteGroup("),
		[]byte("reg.Subscribe("),
	}
	for _, path := range findAllCellInitFiles(root) {
		content, err := os.ReadFile(path) //nolint:gosec // archtest scans repo paths it discovered
		if err != nil {
			continue
		}
		for _, sym := range forbidden {
			if bytes.Contains(content, sym) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("MARKER-MISSING-FOR-WIRE-CALL-01: %s contains %q — wire is owned by cell_gen.go after K#04 opt-in. "+
					"Add `// +cell:listener` / `// +slice:route` / `// +slice:subscribe` markers and run `gocell generate cell %s`",
					filepath.ToSlash(rel), string(sym), deriveCellID(path))
			}
		}
	}
}

// TestMarkergenDriftVerify01 verifies MARKERGEN-DRIFT-VERIFY-01.
// markergen.Merge MUST succeed against the live ProjectMeta — drift between
// cell.go markers and ProjectMeta projection (e.g. duplicate listener decl,
// orphan handler field reference) yields an error.
func TestMarkergenDriftVerify01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("MARKERGEN-DRIFT-VERIFY-01: metadata.Parse failed: %v", err)
	}
	if _, err := markergen.Merge(root, project); err != nil {
		t.Errorf("MARKERGEN-DRIFT-VERIFY-01: markergen.Merge failed: %v — fix marker comments or run `gocell generate cell --verify`", err)
	}
}

// TestMarkerWireSingleSource01 verifies MARKER-WIRE-SINGLE-SOURCE-01.
// For every K#04-opted-in cell (GoStructName != "" and cell_gen.go exists),
// cell.go MUST declare at least one `// +cell:listener` marker. The
// marker is the wire-layer source of truth that drives cell_gen.go's
// generated reg.RouteGroup blocks.
func TestMarkerWireSingleSource01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("MARKER-WIRE-SINGLE-SOURCE-01: metadata.Parse failed: %v", err)
	}
	for _, c := range project.Cells {
		if c.GoStructName == "" {
			continue // not opted in to K#04 codegen
		}
		dir := filepath.Join(root, filepath.Dir(c.File))
		genPath := filepath.Join(dir, "cell_gen.go")
		if _, err := os.Stat(genPath); err != nil {
			continue // CODEGEN-CELL-GEN-01 already catches missing cell_gen.go
		}
		cellGoPath := filepath.Join(dir, "cell.go")
		content, err := os.ReadFile(cellGoPath) //nolint:gosec // archtest scans paths it discovered
		if err != nil {
			t.Errorf("MARKER-WIRE-SINGLE-SOURCE-01: read %s: %v", cellGoPath, err)
			continue
		}
		if !bytes.Contains(content, []byte("// +cell:listener:")) {
			rel, _ := filepath.Rel(root, cellGoPath)
			t.Errorf(
				"MARKER-WIRE-SINGLE-SOURCE-01: %s (cell %q) is K#04 opted-in but declares no "+
					"`// +cell:listener` marker — wire is single-sourced via markers after K#05",
				filepath.ToSlash(rel), c.ID)
		}
	}
}
