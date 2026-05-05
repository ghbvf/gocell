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

// projectFromMetadata parses ProjectMeta from root for the helpers below.
// Returns nil on parse error so callers can fall back to empty results without
// failing the test (callers are best-effort enumerators).
func projectFromMetadata(root string) *metadata.ProjectMeta {
	p, err := metadata.NewParser(root).Parse()
	if err != nil {
		return nil
	}
	return p
}

// findAllCellFiles enumerates cell.go for every cell registered in
// ProjectMeta.Cells (covers both top-level cells/ and examples/*/cells/ via
// path-pattern matching in kernel/metadata/parser.go).
func findAllCellFiles(root string) []string {
	project := projectFromMetadata(root)
	if project == nil {
		return nil
	}
	var files []string
	for _, c := range project.Cells {
		path := filepath.Join(root, filepath.Dir(c.File), "cell.go")
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files
}

// findAllCellInitFiles enumerates cell.go / cell_init.go / cell_routes.go /
// cell_providers.go for every cell registered in ProjectMeta.Cells. Excludes
// cell_gen.go (generated owns wire calls) and *_test.go.
func findAllCellInitFiles(root string) []string {
	project := projectFromMetadata(root)
	if project == nil {
		return nil
	}
	var files []string
	for _, c := range project.Cells {
		cellDir := filepath.Join(root, filepath.Dir(c.File))
		entries, err := os.ReadDir(cellDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if name == "cell_gen.go" {
				continue
			}
			if name == "cell.go" || strings.HasPrefix(name, "cell_") {
				files = append(files, filepath.Join(cellDir, name))
			}
		}
	}
	sort.Strings(files)
	return files
}

// findAllCellYAMLs enumerates cell.yaml for every cell registered in
// ProjectMeta.Cells.
func findAllCellYAMLs(root string) []string {
	project := projectFromMetadata(root)
	if project == nil {
		return nil
	}
	var files []string
	for _, c := range project.Cells {
		files = append(files, filepath.Join(root, c.File))
	}
	sort.Strings(files)
	return files
}

// findAllSliceYAMLs enumerates slice.yaml for every slice registered in
// ProjectMeta.Slices.
func findAllSliceYAMLs(root string) []string {
	project := projectFromMetadata(root)
	if project == nil {
		return nil
	}
	var files []string
	for _, s := range project.Slices {
		files = append(files, filepath.Join(root, s.File))
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

// forbiddenWireCall reports whether the Go source file at path contains a
// call expression of the form reg.RouteGroup(...) or reg.Subscribe(...).
// It uses AST scanning rather than byte-level search to avoid false positives
// from comments that contain the string literal "reg.RouteGroup(" or
// "reg.Subscribe(".
func forbiddenWireCall(path string) (found string, line int, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return "", 0, err
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if recv.Name != "reg" {
			return true
		}
		if sel.Sel.Name == "RouteGroup" || sel.Sel.Name == "Subscribe" {
			found = "reg." + sel.Sel.Name + "("
			line = fset.Position(call.Pos()).Line
		}
		return true
	})
	return found, line, nil
}

// TestMarkerMissingForWireCall01 verifies MARKER-MISSING-FOR-WIRE-CALL-01.
// cell.go / cell_init.go / cell_routes.go MUST NOT call `reg.RouteGroup(`
// or `reg.Subscribe(` — these calls are owned by cell_gen.go's generated
// Init after K#04 opt-in. New routes/subscribes are declared via marker
// comments and rendered into cell_gen.go.
//
// AST-based scanning is used (not byte-level grep) to avoid false positives
// from comments containing the string "reg.RouteGroup(" or "reg.Subscribe(".
func TestMarkerMissingForWireCall01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	for _, path := range findAllCellInitFiles(root) {
		sym, line, err := forbiddenWireCall(path)
		if err != nil {
			continue
		}
		if sym != "" {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("MARKER-MISSING-FOR-WIRE-CALL-01: %s:%d contains %q — wire is owned by cell_gen.go after K#04 opt-in. "+
				"Add `// +cell:listener` / `// +slice:route` / `// +slice:subscribe` markers and run `gocell generate cell %s`",
				filepath.ToSlash(rel), line, sym, deriveCellID(path))
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

// cellGenHasRouteGroup reports whether the given cell_gen.go file contains a
// reg.RouteGroup(...) call expression (AST-based, not byte-level grep).
// Returns false when the file cannot be parsed.
func cellGenHasRouteGroup(genPath string) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, genPath, nil, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if recv.Name == "reg" && sel.Sel.Name == "RouteGroup" {
			found = true
		}
		return true
	})
	return found
}

// TestMarkerWireSingleSource01 verifies MARKER-WIRE-SINGLE-SOURCE-01.
// For every K#04-opted-in cell (GoStructName != "" and cell_gen.go exists)
// where cell_gen.go declares a reg.RouteGroup(...) call, cell.go MUST
// declare at least one `// +cell:listener` marker. Pure-subscribe cells
// (cell_gen.go has no reg.RouteGroup call) are exempt from this requirement.
//
// The check uses AST scanning for cell_gen.go to avoid false positives from
// comments, and bytes.Contains for the cell.go marker prefix scan (markers
// are structured comment annotations, not code).
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
		// Only require listener marker when cell_gen.go actually mounts routes.
		// Pure-subscribe cells have no reg.RouteGroup call and are exempt.
		if !cellGenHasRouteGroup(genPath) {
			continue
		}
		cellGoPath := filepath.Join(dir, "cell.go")
		content, err := os.ReadFile(cellGoPath) //nolint:gosec // archtest scans paths it discovered
		if err != nil {
			t.Errorf("MARKER-WIRE-SINGLE-SOURCE-01: read %s: %v", cellGoPath, err)
			continue
		}
		if !cellGoHasListenerMarker(content) {
			rel, _ := filepath.Rel(root, cellGoPath)
			t.Errorf(
				"MARKER-WIRE-SINGLE-SOURCE-01: %s (cell %q) is K#04 opted-in with reg.RouteGroup in cell_gen.go but declares no "+
					"`// +cell:listener` marker — wire is single-sourced via markers after K#05",
				filepath.ToSlash(rel), c.ID)
		}
	}
}

// cellGoHasListenerMarker reports whether cell.go declares a real
// `// +cell:listener:` annotation as an *ast.Comment node. Bytes inside
// string literals or other AST positions do not count.
//
// ref: kubernetes-sigs/controller-tools pkg/markers/parse.go (markers parsed
// from comment groups, not from source bytes).
func cellGoHasListenerMarker(content []byte) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "cell.go", content, parser.ParseComments)
	if err != nil {
		return false
	}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(strings.TrimSpace(c.Text), "// +cell:listener:") {
				return true
			}
		}
	}
	return false
}

// TestMarkerWireSingleSource01_NegativeFixture_StringLiteralOnly asserts
// scanner rejects a cell.go that contains "// +cell:listener:" only inside
// a string-constant value, not as an actual *ast.Comment. Legacy
// bytes.Contains FALSE-PASSes; AST GREEN refactor must inspect f.Comments.
func TestMarkerWireSingleSource01_NegativeFixture_StringLiteralOnly(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	fixturePath := filepath.Join(archDir, "testdata", "marker_wire_single_source_fixtures", "listener_in_string_literal", "cell.go")
	content, err := os.ReadFile(fixturePath) //nolint:gosec // archtest fixture
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if cellGoHasListenerMarker(content) {
		t.Errorf("MARKER-WIRE-SINGLE-SOURCE-01 negative fixture listener_in_string_literal: " +
			"legacy bytes.Contains FALSE-PASSes on string-constant carrier; AST GREEN " +
			"refactor required (parser.ParseFile(ParseComments) + f.Comments scan)")
	}
}
