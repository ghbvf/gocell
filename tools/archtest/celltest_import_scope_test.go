// INVARIANT: CELLTEST-IMPORT-SCOPE-01: production Go files must not import
// packages matching cells/[a-z]+/[a-z]+test$ — the cells/{X}/{X}test/
// naming pattern designates test-infrastructure packages (cell-level testutil
// with a "*test" suffix segment). This rule is complementary to
// TESTUTIL-BOUNDARY-01 (tools/archtest/testutil_boundary_test.go) which
// guards paths containing a "testutil" segment; CELLTEST-IMPORT-SCOPE-01
// precisely guards the cells/*/*test/ naming form.
package archtest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// celltestImportPathPattern matches module-relative import paths whose final
// segment is `[a-z]+test` under cells/{X}/ (e.g. cells/configcore/configcoretest).
// The trailing `$` anchors the match to the top-level testutil package — nested
// sub-packages (cells/configcore/configcoretest/sub) are intentionally NOT
// matched here; they should be ban-covered by their parent package's
// boundary, which an importer must traverse through the matched root first.
var celltestImportPathPattern = regexp.MustCompile(`^github\.com/[^/]+/[^/]+/cells/[a-z]+/[a-z]+test$`)

// isCellTestImportPath reports whether an absolute import path matches the
// cells/{X}/{X}test naming convention.
//
// Blind spots: the pattern matches the full path literal from the import
// declaration; it does not follow type aliases or build-tag-gated imports.
// Both of those forms are effectively impossible to use as a production import
// without also triggering a Go compilation error, so they are acceptable
// non-coverage.
func isCellTestImportPath(importPath string) bool {
	return celltestImportPathPattern.MatchString(importPath)
}

// TestCelltestImportScope enforces CELLTEST-IMPORT-SCOPE-01:
//
// No production Go file (i.e. NOT *_test.go and NOT in a test-infrastructure
// directory) may import a package whose import path matches
// cells/[a-z]+/[a-z]+test$. Those packages are cell-level testutil packages
// and importing them from production code would embed test fixtures and
// t-bound helpers in a release binary, signaling a layering mistake.
//
// Complementary rules:
//   - TESTUTIL-BOUNDARY-01 (testutil_boundary_test.go): guards "testutil" segment paths.
//   - This rule: guards the cells/*/*test/ naming form (e.g. cells/configcore/configcoretest).
//
// The check is discovery-based: any new cells/{X}/{X}test/ package is
// automatically covered without further edits to this file.
//
// Blind spots (non-coverage outside declared rule scope):
//   - Import aliases that rename the package after import: these require type
//     resolution and are already an extremely rare pattern. If introduced, the
//     rule file itself would need to evolve to typed analysis.
//   - Build-tag-gated imports (//go:build ignore) that never compile: these
//     are not production imports in practice and do not reach a release binary.
func TestCelltestImportScope(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)

	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found — module root may be wrong")

	var violations []string
	for _, f := range allGoFiles {
		// Skip _test.go — they are permitted to import test-infra packages.
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		rel, err := filepath.Rel(root, f)
		require.NoError(t, err)
		rel = filepath.ToSlash(rel)
		// Skip test-infrastructure paths (outboxtest/, configcoretest/, etc.)
		// — they may import sibling test packages.
		if isTestInfraPath(rel) {
			continue
		}

		imports, err := parseImports(f)
		require.NoError(t, err, "failed to parse %s", f)
		for _, imp := range imports {
			if !strings.HasPrefix(imp, modPath+"/") {
				continue
			}
			if isCellTestImportPath(imp) {
				violations = append(violations,
					fmt.Sprintf("CELLTEST-IMPORT-SCOPE-01: %s (production file) imports %s "+
						"(cells/*/*test packages are test-infrastructure; only *_test.go or "+
						"test-infra packages may import them)", rel, imp))
			}
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s", v)
		}
	}
	assert.Empty(t, violations,
		"production (non-_test.go, non-test-infra) files must not import cells/*/*test packages; "+
			"these packages embed test fixtures — import them only from *_test.go or test-infrastructure files")
}

// TestCelltestImportPath_PatternTable is a table-driven unit test for the
// isCellTestImportPath helper. It also acts as the blind-spot self-check test
// required by the AI-rebust chapter: each "outside declared scope" case is
// explicitly listed and asserted to return false (non-matching), confirming the
// rule does NOT attempt to cover those forms.
func TestCelltestImportPath_PatternTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		path      string
		wantMatch bool
	}{
		// Positive: should match (violation)
		{
			name:      "configcoretest direct",
			path:      "github.com/ghbvf/gocell/cells/configcore/configcoretest",
			wantMatch: true,
		},
		{
			name:      "accesscoretest direct",
			path:      "github.com/ghbvf/gocell/cells/accesscore/accesscoretest",
			wantMatch: true,
		},
		{
			name:      "auditcoretest direct",
			path:      "github.com/ghbvf/gocell/cells/auditcore/auditcoretest",
			wantMatch: true,
		},
		// Negative: should NOT match (compliant / out-of-scope)
		{
			name:      "production cell package",
			path:      "github.com/ghbvf/gocell/cells/configcore/slices/flagread",
			wantMatch: false,
		},
		{
			name:      "kernel package",
			path:      "github.com/ghbvf/gocell/kernel/outbox",
			wantMatch: false,
		},
		{
			name:      "outboxtest (kernel, not cells)",
			path:      "github.com/ghbvf/gocell/kernel/outbox/outboxtest",
			wantMatch: false,
		},
		{
			name:      "testutil path (covered by TESTUTIL-BOUNDARY-01)",
			path:      "github.com/ghbvf/gocell/cells/configcore/internal/testutil",
			wantMatch: false,
		},
		{
			name:      "sub-package of celltest (blind spot — outside declared scope)",
			path:      "github.com/ghbvf/gocell/cells/configcore/configcoretest/sub",
			wantMatch: false,
		},
		{
			name:      "runtime package",
			path:      "github.com/ghbvf/gocell/runtime/auth",
			wantMatch: false,
		},
		{
			name:      "examples cell package",
			path:      "github.com/ghbvf/gocell/examples/todoorder/cells/ordercore",
			wantMatch: false,
		},
		{
			name:      "std library",
			path:      "context",
			wantMatch: false,
		},
		{
			name:      "third party",
			path:      "github.com/stretchr/testify/assert",
			wantMatch: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isCellTestImportPath(tc.path)
			assert.Equal(t, tc.wantMatch, got, "isCellTestImportPath(%q)", tc.path)
		})
	}
}
