// INVARIANT: MESSAGE-CONST-LITERAL-01
//
// errcode_message_const_fixtures_test.go — fixture-based regression tests
// for MESSAGE-CONST-LITERAL-01. Fixtures use pure-AST scanning (no
// packages.Load) so they avoid go/types complexity and `replace` directives
// pointing at the main module.
//
// ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md
package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// runErrcodeMessageConstFixtureScan parses every non-test .go file under
// fixtureDir with go/parser only and reports MESSAGE-CONST-LITERAL-01
// violations. The pure-AST mode requires no module resolution, no
// `go/types` info, and works against fixture packages that declare a local
// `errcode` package (see testdata/errcode_message_const/*).
func runErrcodeMessageConstFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	scope := DirsScope(fixtureDir, []string{"."})
	var out []string
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			absPath := p.Abs(file)
			// Skip the local stub errcode pkg; only scan the usage file in the parent.
			if filepath.Base(filepath.Dir(absPath)) == "errcode" {
				continue
			}
			out = append(out, scanErrcodeMessageASTNoTypes(p.Fset, file, p.Rel(file))...)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// scanErrcodeMessageASTNoTypes is the no-types entry point used by fixture
// scanning; it delegates to scanErrcodeMessageAST with a nil TypesInfo so
// the constructor-resolution helper falls back to local-name matching.
func scanErrcodeMessageASTNoTypes(fset *token.FileSet, file *ast.File, rel string) []string {
	return scanErrcodeMessageAST(fset, file, rel, nil)
}

// TestErrcodeMessageConstLiteralFixtures validates the MESSAGE-CONST-
// LITERAL-01 scanner via curated regression cases.
func TestErrcodeMessageConstLiteralFixtures(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "errcode_message_const")

	cases := []struct {
		pkg           string
		wantViolCount int
	}{
		{"compliant", 0},
		{"violates", 5}, // 3 errcode.New/Wrap + httputil.WritePublic + ctxcancel.WrapOrInfra
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			got := runErrcodeMessageConstFixtureScan(t, filepath.Join(base, tc.pkg))
			assert.Equal(t, tc.wantViolCount, len(got),
				"fixture %s: expected %d violation(s), got %d: %v",
				tc.pkg, tc.wantViolCount, len(got), got)
		})
	}
}
