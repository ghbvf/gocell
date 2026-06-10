//go:build archtest

// INVARIANT: AUTH-AUTHTEST-BOUNDARY-01: authtest sub-package is test-only; auth.Authenticated() must stay deleted
//
// Rule logic + helpers live in auth_authtest_boundary.go (CheckAuthAuthtestBoundary)
// so they are importable by an external Cell repo; this _test.go dogfoods the same
// Check (single source) and carries the negative probes proving the detectors are
// not vacuously green. Full rule rationale: auth_authtest_boundary.go.
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

// TestAuthAuthtestBoundary dogfoods CheckAuthAuthtestBoundary against the GoCell
// tree (sub-rules AUTH-AUTHTEST-A + AUTH-AUTHTEST-C).
func TestAuthAuthtestBoundary(t *testing.T) {
	t.Parallel()
	Report(t, "AUTH-AUTHTEST-BOUNDARY-01",
		CheckAuthAuthtestBoundary(t, ConfigForExternalCell{}))
}

// TestAuthAuthtestBoundary_NegativeProbes validates that the rule checks
// themselves work correctly (test-the-test) using synthetic fixtures.
func TestAuthAuthtestBoundary_NegativeProbes(t *testing.T) {
	t.Parallel()

	modPath := PlatformModulePath
	runtimeAuthtestImport := modPath + "/runtime/internal/authtest"
	kernelAuthtestImport := modPath + "/kernel/auth/authtest"

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

	// Probe C1: a non-test file importing runtime/internal/authtest must be caught.
	t.Run("C1_detects_non_test_runtime_authtest_import", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "runtime", "somepackage")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))

		content := fmt.Sprintf("package somepackage\nimport _ %q\n", runtimeAuthtestImport)
		nonTestFile := filepath.Join(pkgDir, "helpers.go") // NOT _test.go
		require.NoError(t, os.WriteFile(nonTestFile, []byte(content), 0o644))

		imports, err := parseImports(nonTestFile)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == runtimeAuthtestImport {
				found = true
			}
		}
		assert.True(t, found, "negative probe C1: parseImports must detect runtime/internal/authtest import in non-test file")
		assert.False(t, strings.HasSuffix(nonTestFile, "_test.go"),
			"negative probe C1: fixture file must not be a _test.go file")
	})

	// Probe C2: a non-test file importing kernel/auth/authtest must also be caught.
	// Mirrors C1 for the second authtest package to ensure AUTH-AUTHTEST-C
	// covers both paths uniformly.
	t.Run("C2_detects_non_test_kernel_authtest_import", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "kernel", "someother")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))

		content := fmt.Sprintf("package someother\nimport _ %q\n", kernelAuthtestImport)
		nonTestFile := filepath.Join(pkgDir, "helpers.go") // NOT _test.go
		require.NoError(t, os.WriteFile(nonTestFile, []byte(content), 0o644))

		imports, err := parseImports(nonTestFile)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == kernelAuthtestImport {
				found = true
			}
		}
		assert.True(t, found, "negative probe C2: parseImports must detect kernel/auth/authtest import in non-test file")
		assert.False(t, strings.HasSuffix(nonTestFile, "_test.go"),
			"negative probe C2: fixture file must not be a _test.go file")
	})
}
