// invariants:
//   - INVARIANT: SESSION-PROTOCOL-COMPOSITION-ROOT-01 (RED fixture coverage)
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestSessionProtocol_RedFixtureDetected asserts SESSION-PROTOCOL-COMPOSITION-ROOT-01
// catches all forms of session.NewProtocol / MustNewProtocol callee in the
// fixture at tools/archtest/internal/sessionprotocolfixture/ (gated by
// `//go:build archtest_fixture`).
//
// # T3 Wave 1 (RED — Soft rule predicate)
//
// The current production rule (commit boundary: before T3 Wave 2) matches on
// `pkg.Name == "session"` Ident — a string anchor. The fixture's 6 banned
// call sites split into three import shapes:
//
//   - qualified-import  `session.NewProtocol(...)`         → caught by Soft
//   - aliased-import    `sess.NewProtocol(...)`            → MISSED by Soft
//   - dot-import        `NewProtocol(...)`                 → MISSED by Soft
//
// Soft catches only the qualified pair (2 hits). This test asserts ≥ 6 hits
// and is therefore RED at Wave 1, GREEN at Wave 2 (typeseval.ResolvePackageRef
// resolves all three callee shapes to the same *types.Func identity).
//
// Reuses the Wave-1 (Soft) predicate inline so the failure mode is
// reproducible without dependencies on Wave 2 helpers. Wave 2 rewrites
// this test to call the shared type-aware scan helper.
func TestSessionProtocol_RedFixtureDetected(t *testing.T) {
	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "internal", "sessionprotocolfixture")

	matches, err := filepath.Glob(filepath.Join(fixtureDir, "*.go"))
	require.NoError(t, err)
	require.NotEmpty(t, matches, "fixture dir must contain *.go files: %s", fixtureDir)

	forbidden := map[string]bool{
		"NewProtocol":     true,
		"MustNewProtocol": true,
	}

	type hit struct {
		file string
		line int
		name string
	}
	var hits []hit

	for _, f := range matches {
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		require.NoErrorf(t, err, "parse fixture %s", f)
		scanner.EachInSubtree[ast.CallExpr](af, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "session" {
				return
			}
			if forbidden[sel.Sel.Name] {
				rel, _ := filepath.Rel(root, f)
				hits = append(hits, hit{
					file: filepath.ToSlash(rel),
					line: fset.Position(call.Pos()).Line,
					name: sel.Sel.Name,
				})
			}
		})
	}

	for _, h := range hits {
		t.Logf("RED fixture hit: %s:%d session.%s", h.file, h.line, h.name)
	}

	// 3 callee shapes × 2 banned function names = 6 expected violations.
	// Soft predicate catches only the qualified pair (NewProtocol +
	// MustNewProtocol), so this test fails until Wave 2's typeseval upgrade.
	assert.GreaterOrEqual(t, len(hits), 6,
		"RED fixture: expected ≥ 6 banned call sites (qualified + aliased + dot × NewProtocol + MustNewProtocol); "+
			"Soft predicate (pkg.Name == \"session\") only catches qualified form. "+
			"This test stays RED until T3 Wave 2 upgrades to typeseval.ResolvePackageRef.")
}
