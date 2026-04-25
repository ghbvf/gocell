package archtest

import (
	"bufio"
	"bytes"
	"fmt"
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

	// AUTH-AUTHTEST-A: ban "auth.Authenticated()" literal in all .go files.
	// Exclude tools/archtest itself (this file references the string in comments
	// and test names) and runtime/auth/authtest (the replacement package whose
	// doc comment necessarily references the deleted function by name).
	t.Run("AUTH-AUTHTEST-A_no_auth_Authenticated_call", func(t *testing.T) {
		hits := grepInDir(t, root, "auth.Authenticated()", "tools/archtest", "runtime/auth/authtest")
		if len(hits) > 0 {
			for _, h := range hits {
				t.Logf("AUTH-AUTHTEST-A violation: %s", h)
			}
		}
		assert.Empty(t, hits,
			"auth.Authenticated() must not appear anywhere in the codebase; "+
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		// Return a best-effort result from raw scanning on parse failure so a
		// single malformed file does not abort the entire walk.
		return rawScanImports(data, path), nil
	}
	var imports []string
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		imports = append(imports, path)
	}
	return imports, nil
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

	// Probe A: grepInDir must detect "auth.Authenticated()" literal in a temp file.
	t.Run("A_detects_auth_Authenticated_call", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		bogus := filepath.Join(tmp, "bogus_test.go")
		if err := os.WriteFile(bogus, []byte("package x\nvar _ = auth.Authenticated()\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		hits := grepInDir(t, tmp, "auth.Authenticated()")
		assert.NotEmpty(t, hits, "negative probe: grepInDir must detect auth.Authenticated() literal in temp file")
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
