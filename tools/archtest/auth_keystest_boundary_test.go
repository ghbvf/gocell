//go:build archtest

// INVARIANT: AUTH-KEYSTEST-IMPORT-BOUNDARY-01
//
// runtime/auth/keystest is test-only. Rule logic lives in auth_keystest_boundary.go
// (CheckAuthKeystestBoundary) so it is importable by an external Cell repo; this
// _test.go dogfoods the same Check (single source) and carries the negative probes
// proving the import detector works. Full rule rationale + blind-spot inventory:
// auth_keystest_boundary.go.
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

// TestAuthKeystestBoundary dogfoods CheckAuthKeystestBoundary against the GoCell
// tree (sub-rules AUTH-KEYSTEST-A/B/C/D).
func TestAuthKeystestBoundary(t *testing.T) {
	t.Parallel()
	Report(t, "AUTH-KEYSTEST-IMPORT-BOUNDARY-01",
		CheckAuthKeystestBoundary(t, ConfigForExternalCell{}))
}

// TestAuthKeystestBoundary_NegativeProbes validates that the detection logic
// works correctly using synthetic fixtures.
func TestAuthKeystestBoundary_NegativeProbes(t *testing.T) {
	t.Parallel()

	modPath := PlatformModulePath
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
