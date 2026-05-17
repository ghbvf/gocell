// INVARIANT: CELLTEST-IMPORT-BOUNDARY-01
//
// # CELLTEST-IMPORT-BOUNDARY-01
//
// The kernel/cell/celltest sub-package is test-only. It provides panic-on-error
// helpers (MustAuthJWT, MustAuthJWTFromAssembly, MustAuthServiceToken) and the
// TestMux / RunRepoReadinessConformance test harness, designed exclusively for
// use in _test.go files and test binaries.
//
// This rule enforces three boundaries:
//
//   - CELLTEST-A: Non-_test.go files must not import kernel/cell/celltest
//     anywhere in the module (exempt: celltest package's own source files, and
//     any _test.go file). Production code must use cell.NewAuthJWT / cell.NewAuthJWTFromAssembly
//     / cell.NewAuthServiceToken (error-first) directly, propagating the error
//     to bootstrap — see ADR docs/architecture/202605171800-adr-kernel-mustctor-removal.md.
//
//   - CELLTEST-B: kernel/** (excluding the celltest package itself and its
//     direct parent kernel/cell/_test.go files) must not import
//     kernel/cell/celltest, including _test.go files. Reason: kernel is
//     a physical-isolation layer that must not depend on test-fixture sub-packages
//     defined inside itself — this would create a test-fixture self-dependency
//     cycle that is invisible to go build but architecturally unsound. A kernel
//     sub-package that needs test helpers should define them as unexported
//     functions in its own _test.go.
//     Exception: kernel/cell/ (the direct parent of celltest) _test.go files are
//     exempt because Go's standard model for testing a sub-package includes the
//     parent package's _test.go verifying the sub-package's panic/error behavior
//     (same pattern as net/http testing httptest helpers).
//
//   - CELLTEST-C: examples/** non-_test.go files must not import
//     kernel/cell/celltest. This mirrors AUTH-KEYSTEST-D: examples are not
//     exempt because the correct production path for constructing AuthPlan values
//     is cell.NewAuthJWT / cell.NewAuthJWTFromAssembly (error-first), as
//     established by cmd/corebundle. Importing celltest in an examples production
//     file is the exact mistake this rule prevents.
//
// # Decision log (why B is kernel-only, not cells/)
//
// cells/** _test.go files legitimately import kernel/cell/celltest to construct
// AuthPlan values (MustAuthJWT etc.) for HTTP handler tests — this is exactly the
// intended K8s httptest-style usage. Prohibiting cells/ _test.go files from
// importing celltest would break dozens of existing handler tests and offer no
// safety benefit (the helpers are _test.go-only and cannot reach production paths
// through standard build). The boundary that matters is:
//
//  1. non-_test.go files (CELLTEST-A) — no layer is exempt;
//  2. kernel/ own files including _test.go (CELLTEST-B) — layering rule;
//  3. examples/ non-_test.go (CELLTEST-C) — mirrors keystest boundary.
//
// A follow-up backlog item CELLTEST-B2-CELLS-EXAMPLES-TESTFILES documents the
// path to extending CELLTEST-B to cover cells/_test.go and examples/_test.go
// if future policy changes (e.g., if examples grow a separate composition root
// that must not use test helpers).
//
// # AI-rebust grade: Medium — import-path string matching via go/parser.
//
// The Hard primary defense is the physical isolation of Must* auth helpers in
// kernel/cell/celltest/: production packages that do not import the package
// cannot access MustAuthJWT*. This archtest is the secondary defense preventing
// non-test files from importing celltest.
//
// # Blind-spot inventory
//
//   - dot-import (`import . "kernel/cell/celltest"`): go/parser still records
//     the import path in f.Imports, so parseImports captures it. No blind spot.
//
//   - blank import (`import _ "kernel/cell/celltest"`): same — path still
//     recorded in f.Imports. No blind spot.
//
//   - Transitive import (a.go → b.go → celltest): this rule only checks direct
//     imports, not transitive closure. A transitive import would require a new
//     production package that directly imports celltest, which would itself
//     violate this rule. No practical blind spot.
//
//   - Build-tag-only files: go/parser falls back to rawScanImports (line-scan
//     fallback) when full parse fails. Import paths are still captured.
//
// ref: ADR `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`
// ref: AUTH-KEYSTEST-IMPORT-BOUNDARY-01 in tools/archtest/auth_keystest_boundary_test.go
// ref: AUTH-AUTHTEST-BOUNDARY-01 in tools/archtest/auth_authtest_boundary_test.go
// backlog: CELLTEST-B2-CELLS-EXAMPLES-TESTFILES — extend CELLTEST-B to cells/_test.go +
//
//	examples/_test.go if test-helper import policy tightens in the future.
package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCelltestImportBoundary enforces three sub-rules that together ensure
// kernel/cell/celltest is never imported from production (non-_test.go) code
// outside the celltest package itself, never imported from kernel/ packages
// (including their _test.go files), and never imported from examples/ production
// files.
func TestCelltestImportBoundary(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	celltestImport := modPath + "/kernel/cell/celltest"

	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found — module root may be wrong")

	celltestPkgDir := filepath.Join(root, "kernel", "cell", "celltest")

	// CELLTEST-A: non-_test.go files must not import celltest (any layer).
	// Exemptions: the celltest package's own source files; _test.go files.
	t.Run("CELLTEST-A_nontest_files_no_celltest_import", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			// _test.go files are explicitly allowed.
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			// celltest package's own source files are exempt.
			if filepath.Dir(f) == celltestPkgDir {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == celltestImport {
					rel, _ := filepath.Rel(root, f)
					rel = filepath.ToSlash(rel)
					violations = append(violations,
						fmt.Sprintf("CELLTEST-A: %s (non-test file) imports %s — "+
							"production code must use cell.NewAuthJWT / cell.NewAuthJWTFromAssembly / "+
							"cell.NewAuthServiceToken (error-first) instead", rel, imp))
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations,
			"non-test .go files must not import kernel/cell/celltest; "+
				"use cell.NewAuth* error-first constructors in production paths")
	})

	// CELLTEST-B: kernel/** must not import celltest, including _test.go files.
	// Exemptions:
	//   - the celltest package's own source files (they ARE the package);
	//   - kernel/cell/ _test.go files (the direct parent package of celltest)
	//     because Go's standard model for testing a sub-package includes the
	//     parent package's _test.go verifying the sub-package's panic/error
	//     behavior (net/http testing httptest helpers pattern).
	// All other kernel packages — including _test.go — must not import celltest.
	celltestParentDir := filepath.Join(root, "kernel", "cell")
	t.Run("CELLTEST-B_kernel_no_celltest_import", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			// Exempt the celltest package itself.
			if filepath.Dir(f) == celltestPkgDir {
				continue
			}
			// Exempt kernel/cell/ _test.go files (direct parent of celltest).
			if filepath.Dir(f) == celltestParentDir && strings.HasSuffix(f, "_test.go") {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if !strings.HasPrefix(rel, "kernel/") {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == celltestImport {
					violations = append(violations,
						fmt.Sprintf("CELLTEST-B: %s imports %s — "+
							"kernel packages (including _test.go) must not import "+
							"kernel/cell/celltest; layering rule: kernel must not depend on "+
							"test-fixture sub-packages defined within itself", rel, imp))
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations,
			"kernel/ packages (other than kernel/cell parent + celltest itself) must not import "+
				"kernel/cell/celltest; define test helpers locally in the package's own _test.go instead")
	})

	// CELLTEST-C: examples/** non-_test.go files must not import celltest.
	// Mirrors AUTH-KEYSTEST-D: examples/ production files must use
	// cell.NewAuthJWT / cell.NewAuthJWTFromAssembly (error-first), not
	// the Must* panic helpers in celltest.
	t.Run("CELLTEST-C_examples_nontest_no_celltest_import", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if !strings.HasPrefix(rel, "examples/") {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == celltestImport {
					violations = append(violations,
						fmt.Sprintf("CELLTEST-C: %s (non-test file) imports %s — "+
							"examples production code must use cell.NewAuth* (error-first) "+
							"instead of celltest panic helpers", rel, imp))
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations,
			"examples/ non-test files must not import kernel/cell/celltest; "+
				"use cell.NewAuth* error-first constructors in production and demo code")
	})
}

// TestCelltestImportBoundary_NegativeProbes validates that the detection logic
// works correctly using synthetic fixtures.
func TestCelltestImportBoundary_NegativeProbes(t *testing.T) {
	t.Parallel()

	const modPath = "github.com/ghbvf/gocell"
	celltestImport := modPath + "/kernel/cell/celltest"

	// Probe A: parseImports must detect a celltest import in a non-test file.
	t.Run("A_detects_nontest_celltest_import", func(t *testing.T) {
		t.Parallel()
		content := fmt.Sprintf("package cellfoo\nimport _ %q\n", celltestImport)
		path := writeTempGoFile(t, "handler.go", content)
		assert.False(t, strings.HasSuffix(path, "_test.go"),
			"negative probe A: fixture must not be a _test.go file")
		imports, err := parseImports(path)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == celltestImport {
				found = true
			}
		}
		assert.True(t, found,
			"negative probe A: parseImports must detect celltest import in a non-test file")
	})

	// Probe B: _test.go files are permitted by CELLTEST-A and CELLTEST-C;
	// confirm parseImports detects the import AND the file is identified as a
	// test file via HasSuffix.
	t.Run("B_test_file_suffix_detection", func(t *testing.T) {
		t.Parallel()
		content := fmt.Sprintf("package cellfoo\nimport _ %q\n", celltestImport)
		path := writeTempGoFile(t, "handler_test.go", content)
		assert.True(t, strings.HasSuffix(path, "_test.go"),
			"negative probe B: fixture path must end in _test.go to confirm skip logic")
		imports, err := parseImports(path)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == celltestImport {
				found = true
			}
		}
		assert.True(t, found,
			"negative probe B: parseImports must detect celltest import even in _test.go files")
	})

	// Probe C: a kernel/ file (including _test.go) importing celltest must be
	// caught by CELLTEST-B.
	t.Run("C_detects_kernel_celltest_import", func(t *testing.T) {
		t.Parallel()
		content := fmt.Sprintf("package kernelfoo\nimport _ %q\n", celltestImport)
		// Even a _test.go file in kernel/ should be detected by CELLTEST-B.
		path := writeTempGoFile(t, "kernel_test.go", content)
		imports, err := parseImports(path)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == celltestImport {
				found = true
			}
		}
		assert.True(t, found,
			"negative probe C: parseImports must detect celltest import in a kernel _test.go file")
	})

	// Probe D: a non-test file in examples/ must be caught by CELLTEST-C.
	t.Run("D_detects_examples_nontest_celltest_import", func(t *testing.T) {
		t.Parallel()
		content := fmt.Sprintf("package exampleapp\nimport _ %q\n", celltestImport)
		path := writeTempGoFile(t, "app.go", content)
		assert.False(t, strings.HasSuffix(path, "_test.go"),
			"negative probe D: fixture must not be a _test.go file")
		imports, err := parseImports(path)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == celltestImport {
				found = true
			}
		}
		assert.True(t, found,
			"negative probe D: parseImports must detect celltest import in examples non-test file")
	})
}
