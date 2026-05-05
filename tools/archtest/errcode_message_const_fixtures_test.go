// errcode_message_const_fixtures_test.go — fixture-based regression tests
// for MESSAGE-CONST-LITERAL-01. Fixtures use pure-AST scanning (no
// packages.Load) so they avoid go/types complexity and `replace` directives
// pointing at the main module.
//
// ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runErrcodeMessageConstFixtureScan parses every non-test .go file under
// fixtureDir with go/parser only and reports MESSAGE-CONST-LITERAL-01
// violations. The pure-AST mode requires no module resolution, no
// `go/types` info, and works against fixture packages that declare a local
// `errcode` package (see testdata/errcode_message_const/*).
func runErrcodeMessageConstFixtureScan(t *testing.T, fixtureDir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(fixtureDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "errcode" {
			// Skip the local stub pkg; only scan the usage file in the parent.
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(fixtureDir, path)
		if relErr != nil {
			rel = path
		}
		out = append(out, scanErrcodeMessageASTNoTypes(fset, file, rel)...)
		return nil
	})
	require.NoError(t, err, "walk fixture %s", fixtureDir)
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
		{"violates", 1},
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
