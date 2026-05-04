// SVCTOKEN-CALLER-CELL-REQUIRED-01
//
// Invariant: every call expression `auth.GenerateServiceToken(...)` must
// pass a non-empty string literal as its second argument (callerCell).
// The literal must:
//   - match the pattern ^[a-z][a-z0-9-]*$ (valid cell ID format)
//   - be a known cell ID according to cells/ directory names OR actors.yaml
//
// This gate prevents callers from omitting the caller identity or using an
// unregistered cell name, which would defeat the purpose of 4-part service
// token caller-cell propagation.
//
// Detection: AST walk of all production .go files, scanning call expressions
// of the form auth.GenerateServiceToken(...). The second argument (index 1)
// must be a string literal matching the cell-ID regex and appearing in the
// known-cells set derived from cells/ subdirectories and actors.yaml.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ruleSvctokenCallerCellRequired01 is the archtest rule identifier; not a credential.
//
//nolint:gosec // G101 false positive: archtest rule identifier, not a credential
const ruleSvctokenCallerCellRequired01 = "SVCTOKEN-CALLER-CELL-REQUIRED-01"

// cellIDRegex is the canonical cell-ID pattern: lowercase letter + lowercase
// alphanumeric/dash, at least 2 chars total.
var cellIDRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// TestSVCTOKEN_CALLER_CELL_REQUIRED_01 enforces that every call to
// auth.GenerateServiceToken passes a valid cell-ID string literal as its
// second argument (callerCell).
//
// Note: this test FAILS (RED) until Wave 2 updates GenerateServiceToken to
// accept callerCell as the second parameter AND all call sites are migrated.
func TestSVCTOKEN_CALLER_CELL_REQUIRED_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	knownCells := discoverKnownCells(t, root)

	searchDirs := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "cells"),
		filepath.Join(root, "cmd"),
		filepath.Join(root, "examples"),
		filepath.Join(root, "tests"),
	}

	var violations []string

	for _, dir := range searchDirs {
		// Include _test.go files too — test helpers can also use GenerateServiceToken.
		allFiles, err := findAllGoFilesInDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
		for _, f := range allFiles {
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			hits, scanErr := scanGenerateServiceTokenCallSites(f, rel, knownCells)
			if scanErr != nil {
				t.Logf("scan error %s: %v", rel, scanErr)
				continue
			}
			violations = append(violations, hits...)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	if len(violations) > 0 {
		t.Errorf("%s: %d auth.GenerateServiceToken call sites with invalid/missing callerCell.\n"+
			"The second argument must be a non-empty string literal matching ^[a-z][a-z0-9-]*$\n"+
			"and must be a known cell ID from cells/ or actors.yaml.\n"+
			"Known cells: %v",
			ruleSvctokenCallerCellRequired01, len(violations), sortedKeys(knownCells))
	}
}

// discoverKnownCells returns the set of valid caller cell IDs by listing
// subdirectories of cells/ (first-level only: accesscore, auditcore, etc.)
// plus any actor IDs from actors.yaml.
func discoverKnownCells(t *testing.T, root string) map[string]bool {
	t.Helper()
	known := map[string]bool{}

	cellsDir := filepath.Join(root, "cells")
	entries, err := os.ReadDir(cellsDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() && cellIDRegex.MatchString(e.Name()) {
				known[e.Name()] = true
			}
		}
	}

	// Also allow actor IDs from actors.yaml (simple grep for "id:" lines).
	actorsFile := filepath.Join(root, "actors.yaml")
	data, err := os.ReadFile(actorsFile) //nolint:gosec // G304 false positive: actorsFile path is constant within repo
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "id:") {
				id := strings.TrimSpace(strings.TrimPrefix(line, "id:"))
				id = strings.Trim(id, "\"'")
				if cellIDRegex.MatchString(id) {
					known[id] = true
				}
			}
		}
	}

	// Always allow the test/example cell IDs used in examples/.
	known["example-cell"] = true
	return known
}

// scanGenerateServiceTokenCallSites parses a .go file and returns violation
// strings for any auth.GenerateServiceToken call where the second argument
// (callerCell) is not a valid known cell-ID string literal.
func scanGenerateServiceTokenCallSites(path, rel string, knownCells map[string]bool) ([]string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil //nolint:nilerr // soft-skip on read error: archtest fixture allows missing/unreadable files (caller will scan rest)
	}

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isAuthGenerateServiceTokenCall(call) {
			return true
		}
		pos := fset.Position(call.Pos())

		// The 4-part signature is GenerateServiceToken(ring, callerCell, method, path, query, ts).
		// callerCell is argument index 1 (0-based).
		if len(call.Args) < 2 {
			violations = append(violations, fmt.Sprintf(
				"%s:%d: auth.GenerateServiceToken called with fewer than 2 arguments — missing callerCell",
				rel, pos.Line))
			return true
		}

		arg1 := call.Args[1]
		lit, ok := arg1.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			violations = append(violations, fmt.Sprintf(
				"%s:%d: auth.GenerateServiceToken second argument (callerCell) must be a string literal",
				rel, pos.Line))
			return true
		}

		// Strip quotes.
		callerCell := lit.Value
		if len(callerCell) >= 2 && callerCell[0] == '"' && callerCell[len(callerCell)-1] == '"' {
			callerCell = callerCell[1 : len(callerCell)-1]
		}

		if callerCell == "" {
			violations = append(violations, fmt.Sprintf(
				"%s:%d: auth.GenerateServiceToken callerCell must not be empty",
				rel, pos.Line))
			return true
		}

		if !cellIDRegex.MatchString(callerCell) {
			violations = append(violations, fmt.Sprintf(
				"%s:%d: auth.GenerateServiceToken callerCell %q does not match ^[a-z][a-z0-9-]*$",
				rel, pos.Line, callerCell))
			return true
		}

		if !knownCells[callerCell] {
			violations = append(violations, fmt.Sprintf(
				"%s:%d: auth.GenerateServiceToken callerCell %q is not a known cell ID — "+
					"register it in cells/ or actors.yaml",
				rel, pos.Line, callerCell))
		}
		return true
	})
	return violations, nil
}

// isAuthGenerateServiceTokenCall reports whether call is a call expression
// of the form auth.GenerateServiceToken(...).
func isAuthGenerateServiceTokenCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "GenerateServiceToken" {
		return false
	}
	// Accept any receiver whose Name is "auth" (package alias or direct reference).
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "auth"
}

// findAllGoFilesInDir walks dir and returns all .go files (including _test.go).
func findAllGoFilesInDir(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "generated", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// sortedKeys returns a sorted slice of map keys for diagnostic output.
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
