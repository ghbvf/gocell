// INVARIANT: AUTH-AUTHTEST-BOUNDARY-01: authtest sub-package is test-only; auth.Authenticated() must stay deleted
package archtest

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthAuthtestBoundary enforces three rules related to the removal of
// auth.Authenticated() and the test-only authtest sub-package:
//
//   - AUTH-AUTHTEST-A: no Go file anywhere in the module may contain the
//     literal call expression "auth.Authenticated()" — this seals the deleted
//     export and prevents accidental reintroduction.
//
//   - AUTH-AUTHTEST-B: cells/** and examples/** files (including _test.go) must
//     not import "runtime/auth/authtest" — production cells and examples must
//     use auth.AnyRole(...) or auth.SelfOr(...), never the test-only helper.
//
//   - AUTH-AUTHTEST-C: non-test Go files (files not ending in _test.go) must
//     not import "runtime/auth/authtest" anywhere in the module — the authtest
//     package is exclusively for _test.go files.
//
// The authtest package itself (runtime/auth/authtest/*.go) is exempt from
// AUTH-AUTHTEST-C (it IS the authtest implementation, not an importer).
func TestAuthAuthtestBoundary(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	authtestImport := modPath + "/runtime/auth/authtest"

	// Collect all .go files once and share across rules.
	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found — module root may be wrong")

	// AUTH-AUTHTEST-A: ban auth.Authenticated() *call expressions* in all .go
	// files. Detection is AST-based (go/parser → ast.Walk → ast.CallExpr with
	// SelectorExpr Fun X.auth, Sel.Authenticated) so that comments, doc strings,
	// commit-message-style log messages, and unrelated identical strings inside
	// string literals are not misclassified as violations. Exclude tools/archtest
	// itself (this file references the symbol in test names and probe content)
	// and runtime/auth/authtest (the replacement package's doc comment names the
	// deleted function — comments are AST-stripped so this is precaution only).
	t.Run("AUTH-AUTHTEST-A_no_auth_Authenticated_call", func(t *testing.T) {
		var hits []string
		for _, f := range allGoFiles {
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "tools/archtest/") || strings.HasPrefix(rel, "runtime/auth/authtest/") {
				continue
			}
			callHits, err := findCallExpr(f, "auth", "Authenticated")
			require.NoErrorf(t, err, "failed to AST-scan %s", f)
			for _, line := range callHits {
				hits = append(hits, fmt.Sprintf("%s:%d", rel, line))
			}
		}
		if len(hits) > 0 {
			for _, h := range hits {
				t.Logf("AUTH-AUTHTEST-A violation (real call expression): %s", h)
			}
		}
		assert.Empty(t, hits,
			"auth.Authenticated() must not be called anywhere in the codebase; "+
				"the function has been deleted — use auth.AnyRole(...) in production, "+
				"authtest.RequireAuthenticated() in runtime _test.go files")
	})

	// AUTH-AUTHTEST-B: cells/**, examples/**, and kernel/** must not import
	// authtest, even in _test.go files — kernel must not depend on runtime/
	// (layering violation), and production/example test code must not
	// depend on runtime test helpers.
	t.Run("AUTH-AUTHTEST-B_cells_examples_kernel_no_authtest_import", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if !strings.HasPrefix(rel, "cells/") && !strings.HasPrefix(rel, "examples/") && !strings.HasPrefix(rel, "kernel/") {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == authtestImport {
					violations = append(violations,
						fmt.Sprintf("AUTH-AUTHTEST-B: %s imports %s (cells/examples/kernel must not import runtime test helpers)", rel, imp))
				}
			}
		}
		if len(violations) > 0 {
			for _, v := range violations {
				t.Logf("%s", v)
			}
		}
		assert.Empty(t, violations,
			"cells/, examples/, and kernel/ must not import runtime/auth/authtest; "+
				"kernel/ must not depend on runtime/ (layering violation); "+
				"use auth.AnyRole(...) or auth.SelfOr(...) for production routes")
	})

	// AUTH-AUTHTEST-C: non-test Go files must not import authtest anywhere.
	// Exception: the authtest package's own source files are excluded (they
	// are the implementation, not consumers).
	t.Run("AUTH-AUTHTEST-C_only_test_files_may_import_authtest", func(t *testing.T) {
		authtestPkgDir := filepath.Join(root, "runtime", "auth", "authtest")
		var violations []string
		for _, f := range allGoFiles {
			// Skip _test.go files — they are permitted by this rule.
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			// Skip the authtest package's own implementation files.
			if filepath.Dir(f) == authtestPkgDir {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == authtestImport {
					rel, _ := filepath.Rel(root, f)
					rel = filepath.ToSlash(rel)
					violations = append(violations,
						fmt.Sprintf("AUTH-AUTHTEST-C: %s (non-test file) imports %s (only _test.go files may import authtest)", rel, imp))
				}
			}
		}
		if len(violations) > 0 {
			for _, v := range violations {
				t.Logf("%s", v)
			}
		}
		assert.Empty(t, violations,
			"non-test .go files must not import runtime/auth/authtest; "+
				"move your auth policy helper into a _test.go file, or use "+
				"auth.TestContext(subject, roles) for cell handler tests")
	})
}

// collectGoFiles walks the module root and returns absolute paths to all *.go
// files, skipping vendor, hidden directories, and the tools/archtest directory
// itself (to avoid archtest scanning its own source for rule violations that
// reference forbidden strings in comments/test names).
func collectGoFiles(root string) ([]string, error) {
	var files []string
	archtestDir := filepath.Join(root, "tools", "archtest")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == "vendor" || (len(name) > 0 && name[0] == '.') {
				return filepath.SkipDir
			}
			// Exclude archtest itself from file collection used by AUTH-AUTHTEST-B/C
			// (AUTH-AUTHTEST-A uses grepInDir with its own exclude list).
			if path == archtestDir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// parseImports parses a single Go source file and returns the list of import
// paths it declares. Uses go/parser for correctness; does not execute any Go
// toolchain commands.
func parseImports(path string) ([]string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		return rawScanImports(data, path), err
	}
	var imports []string
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		imports = append(imports, path)
	}
	return imports, nil
}

// findCallExpr parses path with full AST and returns the line numbers of every
// call expression of the form "<pkg>.<sel>(...)" where the receiver matches
// pkgIdent and the selector matches selName. Comments and string literals do
// not match because they are not represented as ast.CallExpr nodes — the entire
// point of moving rule A from text grep to AST detection is to avoid those
// false positives. Parse failures are returned to make malformed files fail
// the archtest directly.
func findCallExpr(path, pkgIdent, selName string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == pkgIdent && sel.Sel.Name == selName {
			lines = append(lines, fset.Position(call.Lparen).Line)
		}
		return true
	})
	return lines, nil
}

// rawScanImports is a line-scanner fallback used when go/parser fails (e.g.
// build-tag-only files). It extracts quoted import paths from import blocks.
func rawScanImports(data []byte, _ string) []string {
	var imports []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	inImport := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == `import (` || line == "import(" {
			inImport = true
			continue
		}
		if inImport && line == ")" {
			inImport = false
			continue
		}
		if inImport || strings.HasPrefix(line, `import "`) {
			// Extract the quoted path.
			start := strings.Index(line, `"`)
			end := strings.LastIndex(line, `"`)
			if start >= 0 && end > start {
				imports = append(imports, line[start+1:end])
			}
		}
	}
	return imports
}

// TestAuthAuthtestBoundary_NegativeProbes validates that the rule checks
// themselves work correctly (test-the-test) using synthetic fixtures.
func TestAuthAuthtestBoundary_NegativeProbes(t *testing.T) {
	t.Parallel()

	const modPath = "github.com/ghbvf/gocell"
	authtestImport := modPath + "/runtime/auth/authtest"

	// Probe A1: findCallExpr must detect a real auth.Authenticated() call site.
	t.Run("A1_findCallExpr_detects_real_call", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		bogus := filepath.Join(tmp, "bogus_test.go")
		// Real call expression — must be detected.
		if err := os.WriteFile(bogus, []byte("package x\nvar _ = auth.Authenticated()\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		hits, err := findCallExpr(bogus, "auth", "Authenticated")
		require.NoError(t, err)
		assert.NotEmpty(t, hits,
			"negative probe A1: findCallExpr must detect real auth.Authenticated() call expression")
	})

	// Probe A2: findCallExpr must IGNORE the literal text inside string constants
	// and comments (the headline reason rule A was upgraded from grepInDir to
	// AST). Without this guarantee, doc strings or audit-message templates that
	// happen to mention auth.Authenticated() would noise CI red.
	t.Run("A2_findCallExpr_ignores_strings_and_comments", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		decoy := filepath.Join(tmp, "decoy_test.go")
		const decoyContent = `package x
// auth.Authenticated() — historical reference in a comment, must not match.
var msg = "auth.Authenticated() — string literal mentioning the symbol, must not match."
`
		if err := os.WriteFile(decoy, []byte(decoyContent), 0o644); err != nil {
			t.Fatal(err)
		}
		hits, err := findCallExpr(decoy, "auth", "Authenticated")
		require.NoError(t, err)
		assert.Empty(t, hits,
			"negative probe A2: findCallExpr must NOT match auth.Authenticated() inside comments or string literals")
	})

	// Probe B: a cells/ file importing authtest must be caught.
	t.Run("B_detects_cells_authtest_import", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cellsDir := filepath.Join(root, "cells", "fakecell")
		require.NoError(t, os.MkdirAll(cellsDir, 0o755))

		content := fmt.Sprintf("package fakecell\nimport _ %q\n", authtestImport)
		require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "cell_test.go"), []byte(content), 0o644))

		// Parse the file and check if authtest import is found.
		imports, err := parseImports(filepath.Join(cellsDir, "cell_test.go"))
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == authtestImport {
				found = true
			}
		}
		assert.True(t, found, "negative probe B: parseImports must detect the authtest import")
	})

	// Probe C: a non-test file importing authtest must be caught.
	t.Run("C_detects_non_test_authtest_import", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "runtime", "somepackage")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))

		content := fmt.Sprintf("package somepackage\nimport _ %q\n", authtestImport)
		nonTestFile := filepath.Join(pkgDir, "helpers.go") // NOT _test.go
		require.NoError(t, os.WriteFile(nonTestFile, []byte(content), 0o644))

		imports, err := parseImports(nonTestFile)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == authtestImport {
				found = true
			}
		}
		assert.True(t, found, "negative probe C: parseImports must detect authtest import in non-test file")
		assert.False(t, strings.HasSuffix(nonTestFile, "_test.go"),
			"negative probe C: fixture file must not be a _test.go file")
	})
}
