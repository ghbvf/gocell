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

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
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

	// Scan all production roots (cells/, runtime/, cmd/, examples/, tests/),
	// including _test.go since test helpers also call GenerateServiceToken
	// and must declare a known callerCell. Direct walk is correct here:
	// the rule scope is wider than cell-managed code (e.g.,
	// examples/ssobff/walkthrough_test.go is non-cell example code).
	searchDirs := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "cells"),
		filepath.Join(root, "cmd"),
		filepath.Join(root, "examples"),
		filepath.Join(root, "tests"),
	}
	var allFiles []string
	for _, dir := range searchDirs {
		ff, err := findAllGoFilesInDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
		allFiles = append(allFiles, ff...)
	}

	var violations []string
	for _, f := range allFiles {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		hits, scanErr := scanGenerateServiceTokenCallSites(f, rel, knownCells)
		require.NoError(t, scanErr)
		violations = append(violations, hits...)
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

// discoverKnownCells returns the set of valid caller cell IDs from
// ProjectMeta.Cells (covers both top-level and examples cells via
// metadata path-pattern matching) plus actor IDs from actors.yaml.
func discoverKnownCells(t *testing.T, root string) map[string]bool {
	t.Helper()
	known := map[string]bool{}

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("metadata.NewParser: %v", err)
	}
	for id := range project.Cells {
		if cellIDRegex.MatchString(id) {
			known[id] = true
		}
	}
	for _, a := range project.Actors {
		if cellIDRegex.MatchString(a.ID) {
			known[a.ID] = true
		}
	}
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
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	authAliases := authPackageAliases(f)
	if len(authAliases) == 0 {
		return nil, nil // file does not import runtime/auth
	}

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isAuthGenerateServiceTokenCall(call, authAliases) {
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
// of the form <alias>.GenerateServiceToken(...) where <alias> is one of the
// resolved local names for runtime/auth in the current file. This is
// import-aware so renamed imports (`import authpkg "…/runtime/auth"`) are
// still detected.
func isAuthGenerateServiceTokenCall(call *ast.CallExpr, authAliases map[string]struct{}) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "GenerateServiceToken" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, hit := authAliases[id.Name]
	return hit
}

// authPackageAliases returns the set of local package names by which f
// imports github.com/ghbvf/gocell/runtime/auth. Default name is "auth"
// when no rename is given; explicit aliases (`import x "…"`) are honored;
// dot-imports ("." alias) and blank imports ("_") are excluded because
// neither produces an AST `<name>.GenerateServiceToken` call expression.
func authPackageAliases(f *ast.File) map[string]struct{} {
	const target = `"github.com/ghbvf/gocell/runtime/auth"`
	out := map[string]struct{}{}
	for _, imp := range f.Imports {
		if imp.Path == nil || imp.Path.Value != target {
			continue
		}
		name := "auth"
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				continue
			}
			name = imp.Name.Name
		}
		out[name] = struct{}{}
	}
	return out
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
