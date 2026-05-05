package archtest

// refresh_store_ambient_tx_test.go enforces REFRESH-AMBIENT-TX-01:
// adapters/postgres/refresh_store.go must not contain any direct pool.Begin /
// (*pgxpool.Pool).Begin / tx.Begin calls. After B2-A-08, Peek and Rotate
// delegate transaction management to the injected TxRunner; the store itself
// must not acquire transactions directly.
//
// The rule scans the AST for SelectorExpr calls whose Sel.Name is "Begin"
// where the receiver is a known pool-like identifier. It also catches bare
// method calls named "Begin" on any expression, since the only legitimate
// Begin callers in refresh_store.go would be pool or tx variables.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleRefreshAmbientTX01 = "REFRESH-AMBIENT-TX-01"

// TestRefreshAmbientTX01 asserts that adapters/postgres/refresh_store.go
// contains no SelectorExpr calls to ".Begin(...)". After B2-A-08 the store
// relies solely on the injected TxRunner for transaction lifecycle.
func TestRefreshAmbientTX01(t *testing.T) {
	root := findModuleRoot(t)
	rel := "adapters/postgres/refresh_store.go"
	abs := filepath.Join(root, filepath.FromSlash(rel))

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution|parser.ParseComments)
	require.NoError(t, err, "%s: parse failed", rel)

	type violation struct {
		line int
		expr string
	}
	var violations []violation

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Begin" {
			return true
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, violation{
			line: pos.Line,
			expr: "call to .Begin() at line " + string(rune('0'+pos.Line/100%10)) + string(rune('0'+pos.Line/10%10)) + string(rune('0'+pos.Line%10)),
		})
		return true
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s) in %s:", ruleRefreshAmbientTX01, len(violations), rel)
		for _, v := range violations {
			t.Logf("  line %d: .Begin() call — refresh_store must delegate to TxRunner, not acquire transactions directly", v.line)
		}
	}
	assert.Empty(t, violations,
		"%s: %s must not contain .Begin() calls; use injected TxRunner.RunInTx instead (B2-A-08)",
		ruleRefreshAmbientTX01, rel)
}
