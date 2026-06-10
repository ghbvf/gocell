//go:build archtest

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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCelltestImportScope enforces CELLTEST-IMPORT-SCOPE-01 via
// CheckCelltestImportScope (celltest_import_scope.go). The scanner logic
// lives in the non-test file so external Cell repositories can import and run
// the rule without needing GoCell's _test.go compilation.
//
// Complementary rules:
//   - TESTUTIL-BOUNDARY-01 (testutil_boundary_test.go): guards "testutil" segment paths.
//   - This rule: guards the cells/*/*test/ naming form (e.g. cells/configcore/configcoretest).
//
// Blind spots (non-coverage outside declared rule scope):
//   - Import aliases that rename the package after import: these require type
//     resolution and are already an extremely rare pattern. If introduced, the
//     rule file itself would need to evolve to typed analysis.
//   - Build-tag-gated imports (//go:build ignore) that never compile: these
//     are not production imports in practice and do not reach a release binary.
func TestCelltestImportScope(t *testing.T) {
	Report(t, "CELLTEST-IMPORT-SCOPE-01", CheckCelltestImportScope(t, ConfigForExternalCell{}))
}

// TestCelltestImportPath_PatternTable is a table-driven unit test for the
// isCellTestImportPath helper. Inputs are MODULE-RELATIVE import paths (the
// module prefix already stripped — see celltestScopeCheckFile), so the cases do
// NOT carry a leading module path. It also acts as the blind-spot self-check
// test required by the AI-robust chapter: each "outside declared scope" case is
// explicitly listed and asserted to return false (non-matching), confirming the
// rule does NOT attempt to cover those forms.
func TestCelltestImportPath_PatternTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		path      string
		wantMatch bool
	}{
		// Positive: should match (violation) — module-relative form.
		{
			name:      "configcoretest direct",
			path:      "cells/configcore/configcoretest",
			wantMatch: true,
		},
		{
			name:      "accesscoretest direct",
			path:      "cells/accesscore/accesscoretest",
			wantMatch: true,
		},
		{
			name:      "auditcoretest direct",
			path:      "cells/auditcore/auditcoretest",
			wantMatch: true,
		},
		// Negative: should NOT match (compliant / out-of-scope)
		{
			name:      "production cell package",
			path:      "cells/configcore/slices/flagread",
			wantMatch: false,
		},
		{
			name:      "kernel package",
			path:      "kernel/outbox",
			wantMatch: false,
		},
		{
			name:      "outboxtest (kernel, not cells)",
			path:      "kernel/outbox/outboxtest",
			wantMatch: false,
		},
		{
			name:      "testutil path (covered by TESTUTIL-BOUNDARY-01)",
			path:      "cells/configcore/internal/testutil",
			wantMatch: false,
		},
		{
			name:      "sub-package of celltest (blind spot — outside declared scope)",
			path:      "cells/configcore/configcoretest/sub",
			wantMatch: false,
		},
		{
			name:      "runtime package",
			path:      "runtime/auth",
			wantMatch: false,
		},
		{
			name:      "examples cell package",
			path:      "examples/todoorder/cells/ordercore",
			wantMatch: false,
		},
		{
			// Documents the module-relative semantics: a non-stripped absolute
			// import path (with a leading module prefix) must NOT match. The
			// caller is responsible for stripping the module prefix first.
			name:      "un-stripped absolute path does not match (module-relative only)",
			path:      PlatformModulePath + "/cells/configcore/configcoretest",
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

// TestCelltestImportScope_CheckFile drives the full file-level scanner
// celltestScopeCheckFile (the rule body), not just the isCellTestImportPath
// helper. It builds a synthetic module under a temp root whose module path is
// deliberately NON-github (example.test/extmod) so a regression to a
// github-format-coupled matcher (the F1 defect) surfaces as a false negative:
// the RED case would silently produce zero diagnostics. It also pins the
// diagnostic line to the real import line (F7), not a hardcoded 1.
func TestCelltestImportScope_CheckFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const modPath = "example.test/extmod" // NON-github → proves module-path-agnostic

	write := func(rel string, imports ...string) string {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		var b strings.Builder
		b.WriteString("package p\n")
		for _, imp := range imports {
			fmt.Fprintf(&b, "import _ %q\n", imp)
		}
		require.NoError(t, os.WriteFile(abs, []byte(b.String()), 0o644))
		return abs
	}

	celltestImp := modPath + "/cells/bar/bartest"

	cases := []struct {
		name     string
		rel      string
		imports  []string
		wantRel  string // "" = no diagnostic expected
		wantLine int
	}{
		{
			name:    "RED production file imports celltest (non-github module)",
			rel:     "cells/foo/handler.go",
			imports: []string{"context", celltestImp},
			wantRel: "cells/foo/handler.go",
			// package p (1), import context (2), import celltest (3).
			wantLine: 3,
		},
		{
			name:    "GREEN _test.go importing celltest is skipped",
			rel:     "cells/foo/handler_test.go",
			imports: []string{celltestImp},
		},
		{
			name:    "GREEN test-infra (testutil) path is skipped",
			rel:     "cells/foo/internal/testutil/helper.go",
			imports: []string{celltestImp},
		},
		{
			name:    "GREEN non-*test cell package import is not flagged",
			rel:     "cells/foo/svc.go",
			imports: []string{modPath + "/cells/bar/slices/baz"},
		},
		{
			name:    "GREEN import from a different module is not flagged",
			rel:     "cells/foo/ext.go",
			imports: []string{"github.com/other/dep/cells/x/xtest"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			abs := write(tc.rel, tc.imports...)
			diags, err := celltestScopeCheckFile(root, modPath, abs)
			require.NoError(t, err)
			if tc.wantRel == "" {
				assert.Empty(t, diags, "compliant file must produce no diagnostic")
				return
			}
			require.Len(t, diags, 1)
			assert.Equal(t, tc.wantRel, diags[0].Rel)
			assert.Equal(t, tc.wantLine, diags[0].Line,
				"diagnostic must point at the real import line, not a hardcoded 1")
		})
	}
}
