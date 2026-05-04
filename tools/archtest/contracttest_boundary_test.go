package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContracttestBoundary seals the post-pkg contract helper boundary:
// tests/contracttest is test-only, and the legacy pkg/contracts plus
// pkg/contracttest import paths must stay deleted.
func TestContracttestBoundary(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	testContracttestImport := modPath + "/tests/contracttest"
	legacyImports := []string{
		modPath + "/pkg/contracts",
		modPath + "/pkg/contracttest",
	}

	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found - module root may be wrong")

	t.Run("CONTRACTTEST-BOUNDARY-A_only_test_files_import_tests_contracttest", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "tests/contracttest/") {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if imp == testContracttestImport || strings.HasPrefix(imp, testContracttestImport+"/") {
					violations = append(violations,
						fmt.Sprintf("CONTRACTTEST-BOUNDARY-A: %s imports %s (tests/contracttest is for _test.go files only)", rel, imp))
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations, "non-test Go files must not import tests/contracttest")
	})

	t.Run("CONTRACTTEST-BOUNDARY-B_no_legacy_pkg_contract_imports", func(t *testing.T) {
		var violations []string
		for _, f := range allGoFiles {
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				for _, legacy := range legacyImports {
					if imp == legacy || strings.HasPrefix(imp, legacy+"/") {
						rel, _ := filepath.Rel(root, f)
						rel = filepath.ToSlash(rel)
						violations = append(violations,
							fmt.Sprintf("CONTRACTTEST-BOUNDARY-B: %s imports deleted legacy package %s", rel, imp))
					}
				}
			}
		}
		for _, v := range violations {
			t.Logf("%s", v)
		}
		assert.Empty(t, violations, "pkg/contracts and pkg/contracttest import paths must stay deleted")
	})
}
