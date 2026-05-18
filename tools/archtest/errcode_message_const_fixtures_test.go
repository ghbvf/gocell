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
)

// runErrcodeMessageConstFixtureScan parses every non-test .go file under
// fixtureDir with go/parser only and reports MESSAGE-CONST-LITERAL-01
// violations. The pure-AST mode requires no module resolution, no
// `go/types` info, and works against fixture packages that declare a local
// `errcode` package (see testdata/errcode_message_const/*).
func runErrcodeMessageConstFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	scope := DirsScope(fixtureDir, []string{"."})
	var out []Diagnostic
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			absPath := p.Abs(file)
			// Skip the local stub errcode pkg; only scan the usage file in the parent.
			if filepath.Base(filepath.Dir(absPath)) == "errcode" {
				continue
			}
			out = append(out, scanErrcodeMessageASTNoTypesDiags(p.Fset, file, p.Rel(file))...)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// scanErrcodeMessageASTNoTypesDiags is the no-types entry point used by fixture
// scanning; it delegates to scanErrcodeMessageASTDiags with a nil TypesInfo so
// the constructor-resolution helper falls back to local-name matching.
func scanErrcodeMessageASTNoTypesDiags(fset *token.FileSet, file *ast.File, rel string) []Diagnostic {
	return scanErrcodeMessageASTDiags(fset, file, rel, nil)
}

// TestErrcodeMessageConstLiteralFixtures validates the MESSAGE-CONST-
// LITERAL-01 scanner via curated regression cases.
//
// Each fixture directory owns a diag.golden capturing the rule's real output
// (Rel:Line: Message); GREEN fixtures have an empty golden. Expected line
// numbers live in the regenerated golden, never in this table. See ADR
// docs/architecture/202605181200-adr-archtest-fixture-diagnostic-golden.md.
func TestErrcodeMessageConstLiteralFixtures(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "errcode_message_const")

	// GREEN dir: empty diag.golden. RED dir: expected diagnostics captured in diag.golden.
	dirs := []string{"compliant", "violates"}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(base, dir)
			got := runErrcodeMessageConstFixtureScan(t, fixtureDir)
			AssertGolden(t, filepath.Join(fixtureDir, "diag.golden"), got)
		})
	}
}
