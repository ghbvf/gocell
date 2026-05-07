package archtest

// IDEMPOTENCY-LUA-HASHTAG-01 — invariant-driven gate.
//
// Invariant: adapters/redis IdempotencyClaimer constructs two Redis Cluster
// keys per dual-KEY Lua EVAL (claim, commit). Both keys MUST be in the
// same Redis Cluster slot, otherwise the cluster rejects the EVAL with
// CROSSSLOT. The shared-slot guarantee is achieved by wrapping the business
// key in a Redis hashtag so CRC16 hashes only the business-key portion.
//
// Since PR-V1-REDIS-KEYNS the key derivation is funneled through
// KeyNamespace.applyHashtag, which produces:
//
//	<ns>:{<key>}:<role>
//
// The KeyNamespace prefix sits OUTSIDE the hashtag so it does not affect
// CRC16 slot computation; lease and done keys still colocate. This gate
// asserts that idempotency.go derives leaseKey / doneKey from
// applyHashtag with the role literals "lease" and "done" respectively.
// A regression to manual concatenation (or to a different role literal)
// fails this test.
//
// Mock dispatch is a sibling concern: adapters/redis/mock_test.go must
// recognize the new key shape via suffix matching (":done" / ":lease").
// A regression to prefix matching ("done:" / "lease:") would silently
// pass unit tests against a stale mock. This gate also pins that
// suffix-matched dispatch.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B10, B11
// ref: Redis cluster-spec hash-tags — {tag} sub-string colocation rule

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIdempotency_LuaHashtag verifies the production code in
// adapters/redis/idempotency.go assigns leaseKey / doneKey by calling
// `<receiver>.ns.applyHashtag(key, "<role>")`. The check is structural
// over the AST so renaming `key` to `businessKey` would still pass, but
// reverting to a non-hashtag expression like `"lease:" + key` would fail.
func TestIdempotency_LuaHashtag(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "adapters", "redis", "idempotency.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	require.NoError(t, err, "parse %s", path)

	leaseOK := false
	doneOK := false

	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		switch ident.Name {
		case "leaseKey":
			if isApplyHashtagCall(assign.Rhs[0], "lease") {
				leaseOK = true
			}
		case "doneKey":
			if isApplyHashtagCall(assign.Rhs[0], "done") {
				doneOK = true
			}
		}
		return true
	})

	assert.True(t, leaseOK,
		"adapters/redis/idempotency.go: leaseKey must be derived from "+
			"<receiver>.ns.applyHashtag(key, \"lease\") so the namespace+hashtag "+
			"derivation stays single-source (Redis Cluster slot colocation)")
	assert.True(t, doneOK,
		"adapters/redis/idempotency.go: doneKey must be derived from "+
			"<receiver>.ns.applyHashtag(key, \"done\") so the namespace+hashtag "+
			"derivation stays single-source (Redis Cluster slot colocation)")
}

// isApplyHashtagCall checks whether expr is a method call of the shape
// `<receiver>.ns.applyHashtag(<keyParam>, "<role>")`. The receiver chain
// is allowed to be any selector chain ending in `.ns.applyHashtag` so the
// claimer's struct field name (`ns`) is the only fixed part — renaming
// the outer receiver (`c` → `claimer`) does not break the gate.
//
// The first argument MUST be a plain identifier (not a literal, not a
// composite expression). This catches a regression where a hardcoded
// string sneaks into the hashtag — e.g. `c.ns.applyHashtag("", "lease")`
// — which would silently disable per-call slot colocation.
func isApplyHashtagCall(expr ast.Expr, wantRole string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	outer, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || outer.Sel.Name != "applyHashtag" {
		return false
	}
	inner, ok := outer.X.(*ast.SelectorExpr)
	if !ok || inner.Sel.Name != "ns" {
		return false
	}
	if _, ok := call.Args[0].(*ast.Ident); !ok {
		return false
	}
	role, ok := stringLit(call.Args[1])
	if !ok {
		return false
	}
	return role == wantRole
}

func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	raw := lit.Value
	if len(raw) < 2 {
		return "", false
	}
	first, last := raw[0], raw[len(raw)-1]
	if (first == '"' && last == '"') || (first == '`' && last == '`') {
		return raw[1 : len(raw)-1], true
	}
	return "", false
}

// TestIdempotency_MockDispatchSuffixMatch confirms adapters/redis/mock_test.go
// dispatches the claim-vs-commit Lua scripts by `:done` / `:lease` suffix
// matching, not by legacy `done:` / `lease:` prefix matching. A regression
// here would cause unit tests to silently pass against the wrong dispatch
// branch even after the production key naming changes.
func TestIdempotency_MockDispatchSuffixMatch(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "adapters", "redis", "mock_test.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	require.NoError(t, err, "parse %s", path)

	hasSuffixDone := false
	hasSuffixLease := false
	hasPrefixDone := false
	hasPrefixLease := false

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "strings" {
			return true
		}
		if len(call.Args) != 2 {
			return true
		}
		arg, ok := stringLit(call.Args[1])
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "HasSuffix":
			if arg == ":done" {
				hasSuffixDone = true
			}
			if arg == ":lease" {
				hasSuffixLease = true
			}
		case "HasPrefix":
			if arg == "done:" {
				hasPrefixDone = true
			}
			if arg == "lease:" {
				hasPrefixLease = true
			}
		}
		return true
	})

	assert.True(t, hasSuffixDone,
		"mock_test.go must dispatch claim Lua via strings.HasSuffix(_, \":done\") "+
			"to match the cluster-safe key naming")
	assert.True(t, hasSuffixLease,
		"mock_test.go must dispatch commit Lua via strings.HasSuffix(_, \":lease\") "+
			"to match the cluster-safe key naming")
	assert.False(t, hasPrefixDone,
		"mock_test.go must NOT use legacy strings.HasPrefix(_, \"done:\") dispatch — "+
			"that pattern hides regressions in the cluster hashtag fix")
	assert.False(t, hasPrefixLease,
		"mock_test.go must NOT use legacy strings.HasPrefix(_, \"lease:\") dispatch — "+
			"that pattern hides regressions in the cluster hashtag fix")
}
