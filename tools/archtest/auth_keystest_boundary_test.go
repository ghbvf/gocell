// INVARIANT: AUTH-KEYSTEST-IMPORT-BOUNDARY-01
//
// # AUTH-KEYSTEST-IMPORT-BOUNDARY-01
//
// The runtime/auth/keystest sub-package is test-only. It provides ephemeral
// RSA key-pair helpers (MustGenerateKeyPair, MustNewKeySet, MustNewKeyProvider)
// that panic on RNG failure and are designed exclusively for use in _test.go
// files and test binaries.
//
// This rule enforces four boundaries:
//
//   - AUTH-KEYSTEST-A: kernel/** must not import keystest (kernel must not
//     depend on runtime/ — hard layering violation).
//
//   - AUTH-KEYSTEST-B: cells/** non-_test.go files must not import keystest
//     (cells must not depend on test-only helpers in production code paths).
//
//   - AUTH-KEYSTEST-C: runtime/** non-_test.go files must not import keystest
//     (production runtime code must use auth.GenerateRSAKeyPair() instead).
//
//   - AUTH-KEYSTEST-D: adapters/**, cmd/**, and examples/** non-_test.go files
//     must not import keystest. examples/ is intentionally NOT exempt: the
//     correct production path for ephemeral keys in examples is
//     auth.GenerateRSAKeyPair() (error-first) as established by
//     cmd/corebundle/secrets.go and examples/ssobff/app.go. Importing keystest
//     in an examples production file is the exact mistake this rule prevents.
//
// Exemptions:
//   - The keystest package's own source files (runtime/auth/keystest/).
//   - Any _test.go file anywhere in the module (_test.go files may import
//     keystest for test fixture construction).
//
// AI-rebust grade: Medium — import-path string matching via go/parser. The
// Hard primary defense is the physical isolation of Must* key helpers in
// runtime/auth/keystest/: production packages that do not import the package
// cannot access MustGenerateKeyPair. This archtest is the secondary defense
// preventing non-test files from importing keystest.
//
// Blind-spot inventory:
//
//   - dot-import (`import . "runtime/auth/keystest"`): go/parser still records
//     the import path in f.Imports, so parseImports captures it. No blind spot.
//
//   - blank import (`import _ "runtime/auth/keystest"`): same — path still
//     recorded in f.Imports. No blind spot.
//
//   - Transitive import (a.go → b.go → keystest): this rule only checks direct
//     imports, not transitive closure. A transitive import would require adding
//     a new production package that imports keystest, which would itself violate
//     this rule. No practical blind spot.
//
//   - Build-tag-only files: go/parser falls back to rawScanImports (line-scan
//     fallback) when full parse fails. Import paths are still captured.
//
// ref: ADR `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`
// ref: AUTH-AUTHTEST-BOUNDARY-01 in tools/archtest/auth_authtest_boundary_test.go
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthKeystestBoundary enforces four sub-rules that together ensure
// runtime/auth/keystest is never imported from production (non-_test.go) code
// outside the keystest package itself.
func TestAuthKeystestBoundary(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	keystestImport := modPath + "/runtime/auth/keystest"

	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found — module root may be wrong")

	keystestPkgDir := filepath.Join(root, "runtime", "auth", "keystest")

	// AUTH-KEYSTEST-A: kernel/** must not import keystest (any file, including
	// _test.go), because kernel must not depend on runtime/.
	t.Run("AUTH-KEYSTEST-A_kernel_no_keystest_import", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if !strings.HasPrefix(rel, "kernel/") {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == keystestImport {
					violations = append(violations,
						fmt.Sprintf("AUTH-KEYSTEST-A: %s imports %s (kernel must not depend on runtime/)", rel, imp))
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations,
			"kernel/ must not import runtime/auth/keystest; kernel must not depend on runtime/")
	})

	// AUTH-KEYSTEST-B: cells/** non-_test.go files must not import keystest.
	t.Run("AUTH-KEYSTEST-B_cells_nontest_no_keystest_import", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if !strings.HasPrefix(rel, "cells/") {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == keystestImport {
					violations = append(violations,
						fmt.Sprintf("AUTH-KEYSTEST-B: %s (non-test file) imports %s (cells production code must not import test-only key helpers)", rel, imp))
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations,
			"cells/ non-test files must not import runtime/auth/keystest; "+
				"use auth.GenerateRSAKeyPair() for production ephemeral keys")
	})

	// AUTH-KEYSTEST-C: runtime/** non-_test.go files must not import keystest,
	// except the keystest package's own implementation files.
	t.Run("AUTH-KEYSTEST-C_runtime_nontest_no_keystest_import", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			// Exempt the keystest package itself.
			if filepath.Dir(f) == keystestPkgDir {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if !strings.HasPrefix(rel, "runtime/") {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == keystestImport {
					violations = append(violations,
						fmt.Sprintf("AUTH-KEYSTEST-C: %s (non-test file) imports %s (use auth.GenerateRSAKeyPair)", rel, imp))
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations,
			"runtime/ non-test files must not import runtime/auth/keystest; "+
				"use auth.GenerateRSAKeyPair() for production ephemeral key generation")
	})

	// AUTH-KEYSTEST-D: adapters/**, cmd/**, and examples/** non-_test.go files
	// must not import keystest. examples/ is NOT exempt because the correct
	// pattern for demo ephemeral keys is auth.GenerateRSAKeyPair() (established
	// by cmd/corebundle/secrets.go and examples/ssobff/app.go).
	t.Run("AUTH-KEYSTEST-D_adapters_cmd_examples_nontest_no_keystest_import", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if !strings.HasPrefix(rel, "adapters/") &&
				!strings.HasPrefix(rel, "cmd/") &&
				!strings.HasPrefix(rel, "examples/") {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == keystestImport {
					violations = append(violations,
						fmt.Sprintf("AUTH-KEYSTEST-D: %s (non-test file) imports %s (use auth.GenerateRSAKeyPair)", rel, imp))
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations,
			"adapters/, cmd/, and examples/ non-test files must not import runtime/auth/keystest; "+
				"use auth.GenerateRSAKeyPair() for ephemeral key generation in production and demo code")
	})
}

// TestAuthKeystestBoundary_NegativeProbes validates that the detection logic
// works correctly using synthetic fixtures.
func TestAuthKeystestBoundary_NegativeProbes(t *testing.T) {
	t.Parallel()

	const modPath = "github.com/ghbvf/gocell"
	keystestImport := modPath + "/runtime/auth/keystest"

	// Probe A: parseImports must detect a keystest import in a kernel file.
	t.Run("A_detects_kernel_keystest_import", func(t *testing.T) {
		t.Parallel()
		content := fmt.Sprintf("package kernelfoo\nimport _ %q\n", keystestImport)
		imports, err := parseImports(writeTempGoFile(t, "kernel_foo.go", content))
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == keystestImport {
				found = true
			}
		}
		assert.True(t, found,
			"negative probe A: parseImports must detect keystest import in kernel file")
	})

	// Probe B: _test.go files must not be flagged by rules B/C/D (they are
	// explicitly allowed to import keystest). Verify parseImports returns the
	// import AND the file is identified as a test file via HasSuffix.
	t.Run("B_test_file_suffix_detection", func(t *testing.T) {
		t.Parallel()
		content := fmt.Sprintf("package cellfoo\nimport _ %q\n", keystestImport)
		path := writeTempGoFile(t, "cell_test.go", content)
		assert.True(t, strings.HasSuffix(path, "_test.go"),
			"negative probe B: fixture path must end in _test.go to confirm skip logic")
		imports, err := parseImports(path)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == keystestImport {
				found = true
			}
		}
		assert.True(t, found,
			"negative probe B: parseImports must detect keystest import even in _test.go files")
	})

	// Probe C: non-test file in examples/ must be caught.
	t.Run("C_detects_examples_nontest_keystest_import", func(t *testing.T) {
		t.Parallel()
		content := fmt.Sprintf("package exampleapp\nimport _ %q\n", keystestImport)
		path := writeTempGoFile(t, "app.go", content)
		assert.False(t, strings.HasSuffix(path, "_test.go"),
			"negative probe C: fixture must not be a _test.go file")
		imports, err := parseImports(path)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == keystestImport {
				found = true
			}
		}
		assert.True(t, found,
			"negative probe C: parseImports must detect keystest import in examples non-test file")
	})
}

// writeTempGoFile writes content to a file named filename inside t.TempDir()
// and returns the absolute path. The file is automatically removed when the
// test ends.
func writeTempGoFile(t *testing.T, filename, content string) string {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTempGoFile: %v", err)
	}
	return path
}
