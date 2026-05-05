package archtest

// IDEMPOTENCY-LUA-HASHTAG-01 — invariant-driven gate.
//
// Invariant: adapters/redis IdempotencyClaimer constructs two Redis Cluster
// keys per dual-KEY Lua EVAL (claim, commit). Both keys MUST be in the
// same Redis Cluster slot, otherwise the cluster rejects the EVAL with
// CROSSSLOT. The shared-slot guarantee is achieved by wrapping the business
// key in a Redis hashtag so CRC16 hashes only the business-key portion:
//
//   leaseKey := "{" + key + "}:lease"
//   doneKey  := "{" + key + "}:done"
//
// Without the hashtag (e.g. accidental refactor back to "lease:" + key),
// the keys hash independently and Cluster mode breaks at runtime. This
// gate fails when the construction line in adapters/redis/idempotency.go
// no longer follows the "{" + key + "}:<role>" pattern.
//
// Mock dispatch is a sibling concern: adapters/redis/mock_test.go must
// recognize the new key shape via suffix matching (":done" / ":lease").
// A regression to prefix matching ("done:" / "lease:") would silently
// pass unit tests against a stale mock. This gate also pins that
// suffix-matched dispatch.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B10
// ref: Redis cluster-spec hash-tags — {tag} sub-string colocation rule

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIdempotency_LuaHashtag verifies the production code in
// adapters/redis/idempotency.go assigns leaseKey / doneKey using the
// hashtag-wrapped pattern `"{" + key + "}:lease"` / `"{" + key + "}:done"`.
//
// The check is structural over the AST (not regex over source) so renaming
// `key` to `businessKey` would still pass, but reverting to a non-hashtag
// expression like `"lease:" + key` would fail.
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
			if isHashtagConcat(assign.Rhs[0], ":lease") {
				leaseOK = true
			}
		case "doneKey":
			if isHashtagConcat(assign.Rhs[0], ":done") {
				doneOK = true
			}
		}
		return true
	})

	assert.True(t, leaseOK,
		"adapters/redis/idempotency.go: leaseKey assignment must follow \"{\" + key + \"}:lease\" "+
			"hashtag pattern (Redis Cluster slot colocation)")
	assert.True(t, doneOK,
		"adapters/redis/idempotency.go: doneKey assignment must follow \"{\" + key + \"}:done\" "+
			"hashtag pattern (Redis Cluster slot colocation)")
}

// isHashtagConcat checks whether expr is a string concatenation of the
// shape `"{" + <ident> + "}<suffix>"` where suffix is the role suffix.
// Accepts associativity in either direction (Go parser builds left-leaning
// trees but we tolerate either).
func isHashtagConcat(expr ast.Expr, wantSuffix string) bool {
	parts := flattenAdd(expr)
	if len(parts) < 3 {
		return false
	}
	// Find a literal "{" followed (eventually) by a literal that ends in
	// "}" + wantSuffix. Anything in between must be either ident or further
	// concat — but we just need to confirm the shape's outer brackets.
	first, ok := stringLit(parts[0])
	if !ok || first != "{" {
		return false
	}
	last, ok := stringLit(parts[len(parts)-1])
	if !ok {
		return false
	}
	want := "}" + wantSuffix
	return strings.HasPrefix(last, want)
}

func flattenAdd(expr ast.Expr) []ast.Expr {
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok || bin.Op != token.ADD {
		return []ast.Expr{expr}
	}
	left := flattenAdd(bin.X)
	right := flattenAdd(bin.Y)
	return append(left, right...)
}

func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	// Strip surrounding quotes; tolerate both `"` and backtick raw strings.
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
