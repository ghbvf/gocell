// INVARIANT: CELLTEST-IMPORT-SCOPE-01: production Go files must not import
// packages matching cells/[a-z]+/[a-z]+test$ — the cells/{X}/{X}test/
// naming pattern designates test-infrastructure packages (cell-level testutil
// with a "*test" suffix segment). This rule is complementary to
// TESTUTIL-BOUNDARY-01 (tools/archtest/testutil_boundary_test.go) which
// guards paths containing a "testutil" segment; CELLTEST-IMPORT-SCOPE-01
// precisely guards the cells/*/*test/ naming form.
package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
// isCellTestImportPath helper. It also acts as the blind-spot self-check test
// required by the AI-robust chapter: each "outside declared scope" case is
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
			path:      PlatformModulePath + "/cells/configcore/configcoretest",
			wantMatch: true,
		},
		{
			name:      "accesscoretest direct",
			path:      PlatformModulePath + "/cells/accesscore/accesscoretest",
			wantMatch: true,
		},
		{
			name:      "auditcoretest direct",
			path:      PlatformModulePath + "/cells/auditcore/auditcoretest",
			wantMatch: true,
		},
		// Negative: should NOT match (compliant / out-of-scope)
		{
			name:      "production cell package",
			path:      PlatformModulePath + "/cells/configcore/slices/flagread",
			wantMatch: false,
		},
		{
			name:      "kernel package",
			path:      PlatformModulePath + "/kernel/outbox",
			wantMatch: false,
		},
		{
			name:      "outboxtest (kernel, not cells)",
			path:      PlatformModulePath + "/kernel/outbox/outboxtest",
			wantMatch: false,
		},
		{
			name:      "testutil path (covered by TESTUTIL-BOUNDARY-01)",
			path:      PlatformModulePath + "/cells/configcore/internal/testutil",
			wantMatch: false,
		},
		{
			name:      "sub-package of celltest (blind spot — outside declared scope)",
			path:      PlatformModulePath + "/cells/configcore/configcoretest/sub",
			wantMatch: false,
		},
		{
			name:      "runtime package",
			path:      PlatformModulePath + "/runtime/auth",
			wantMatch: false,
		},
		{
			name:      "examples cell package",
			path:      PlatformModulePath + "/examples/todoorder/cells/ordercore",
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
