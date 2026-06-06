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
//     any _test.go file). Production code must use auth.NewAuthJWT / auth.NewAuthJWTFromAssembly
//     / auth.NewAuthServiceToken (error-first) directly, propagating the error
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
//     is auth.NewAuthJWT / auth.NewAuthJWTFromAssembly (error-first), as
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
// CELLTEST-B currently covers kernel/ own files; cells/_test.go and
// examples/_test.go are out of scope for this rule.
//
// # AI-robust grade: Medium — import-path string matching via go/parser.
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
// CELLTEST-B2-CELLS-EXAMPLES-TESTFILES: extend CELLTEST-B to cells/_test.go +
//
//	examples/_test.go if test-helper import policy tightens in the future.
package archtest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCelltestImportBoundary enforces CELLTEST-IMPORT-BOUNDARY-01 (A/B/C) via
// CheckCelltestImportBoundary (celltest_import_boundary.go). The scanner logic
// lives in the non-test file so external Cell repositories can import and run
// the rule without needing GoCell's _test.go compilation.
func TestCelltestImportBoundary(t *testing.T) {
	Report(t, "CELLTEST-IMPORT-BOUNDARY-01", CheckCelltestImportBoundary(t, ConfigForExternalCell{}))
}

// TestCelltestImportBoundary_NegativeProbes validates that the detection logic
// works correctly using synthetic fixtures.
func TestCelltestImportBoundary_NegativeProbes(t *testing.T) {
	t.Parallel()

	celltestImport := PlatformModulePath + "/kernel/cell/celltest"

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
