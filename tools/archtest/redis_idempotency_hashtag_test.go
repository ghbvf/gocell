//go:build archtest

package archtest

// INVARIANT: IDEMPOTENCY-LUA-HASHTAG-01
//
// IDEMPOTENCY-LUA-HASHTAG-01 — invariant-driven gate.
//
// Invariant: adapters/redis IdempotencyClaimer (idempotency.go) and
// HTTPIdempotencyStore (http_idempotency.go) each construct two Redis Cluster
// keys per dual-KEY Lua EVAL. Both keys MUST be in the same Redis Cluster
// slot, otherwise the cluster rejects the EVAL with CROSSSLOT. The shared-slot
// guarantee is achieved by wrapping the business key in a Redis hashtag so
// CRC16 hashes only the business-key portion.
//
// Since PR-V1-REDIS-KEYNS the key derivation is funneled through
// KeyNamespace.applyHashtag, which produces:
//
//	<ns>:{<key>}:<role>
//
// The KeyNamespace prefix sits OUTSIDE the hashtag so it does not affect
// CRC16 slot computation; lease and done keys still colocate.
//
// This gate asserts two files:
//
//  1. adapters/redis/idempotency.go (event IdempotencyClaimer): leaseKey /
//     doneKey are derived via <receiver>.ns.applyHashtag(key, "lease") /
//     <receiver>.ns.applyHashtag(key, "done") (struct-field chain form).
//
//  2. adapters/redis/http_idempotency.go (HTTP HTTPIdempotencyStore): leaseKey
//     / respKey are derived via KeyNamespace(scopedNS).applyHashtag(key, "lease")
//     / KeyNamespace(scopedNS).applyHashtag(key, "resp") (type-conversion chain
//     form). scopedNS folds the construction-time owner namespace and the
//     request-time tenant ns into "<owner>:<tenant>", so the HTTP store's full
//     key is <owner>:<tenant>:{<key>}:<role> — both prefix segments sit outside
//     the hashtag, leaving CRC16 slot colocation intact. The gate only requires
//     the receiver be a KeyNamespace(<ident>) conversion, so the scopedNS ident
//     still matches.
//
// A regression to manual concatenation (or to a different role literal) in
// either file fails this test.
//
// Mock dispatch is a sibling concern: adapters/redis/mock_test.go must
// recognize the new key shape via suffix matching (":done" / ":lease").
// A regression to prefix matching ("done:" / "lease:") would silently
// pass unit tests against a stale mock. This gate also pins that
// suffix-matched dispatch.
//
// AI-robust rating: Medium. The assertion is structural over AST (no type
// information), so renaming the business-key variable still passes, but
// reverting to a non-hashtag expression like "lease:" + key fails. Consistent
// with the existing gate on idempotency.go.
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

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
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

	scanner.EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
		if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return
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

// TestHTTPIdempotency_LuaHashtag verifies the production code in
// adapters/redis/http_idempotency.go assigns respKey / leaseKey / fpKey by
// calling `KeyNamespace(<ns>).applyHashtag(<keyParam>, "<role>")`. This is the
// type-conversion chain form (KeyNamespace(ns).applyHashtag) rather than the
// struct-field chain form (<receiver>.ns.applyHashtag) used by idempotency.go.
// Both forms are covered under IDEMPOTENCY-LUA-HASHTAG-01.
//
// All three keys passed to the multi-key Lua EVAL (KEYS = {respKey, leaseKey,
// fpKey}) MUST carry the same {key} hash-tag so Redis Cluster colocates them on
// one slot — a CROSSSLOT error otherwise breaks Claim at runtime. fpKey (the
// fingerprint key, added with the same-key reuse guard) is the third KEY and is
// locked here so a future regression that drops its hash-tag fails CI.
//
// The check is structural over the AST so renaming `key` to `businessKey`
// would still pass, but reverting to a non-hashtag expression like
// `"resp:" + key` would fail.
func TestHTTPIdempotency_LuaHashtag(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "adapters", "redis", "http_idempotency.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	require.NoError(t, err, "parse %s", path)

	leaseOK := false
	respOK := false
	fpOK := false

	scanner.EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
		if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return
		}
		switch ident.Name {
		case "leaseKey":
			if isTypeConvApplyHashtagCall(assign.Rhs[0], "lease") {
				leaseOK = true
			}
		case "respKey":
			if isTypeConvApplyHashtagCall(assign.Rhs[0], "resp") {
				respOK = true
			}
		case "fpKey":
			if isTypeConvApplyHashtagCall(assign.Rhs[0], "fp") {
				fpOK = true
			}
		}
	})

	assert.True(t, leaseOK,
		"adapters/redis/http_idempotency.go: leaseKey must be derived from "+
			"KeyNamespace(<ns>).applyHashtag(key, \"lease\") so the namespace+hashtag "+
			"derivation stays single-source (Redis Cluster slot colocation)")
	assert.True(t, respOK,
		"adapters/redis/http_idempotency.go: respKey must be derived from "+
			"KeyNamespace(<ns>).applyHashtag(key, \"resp\") so the namespace+hashtag "+
			"derivation stays single-source (Redis Cluster slot colocation)")
	assert.True(t, fpOK,
		"adapters/redis/http_idempotency.go: fpKey must be derived from "+
			"KeyNamespace(<ns>).applyHashtag(key, \"fp\") so all three Lua KEYS "+
			"({respKey, leaseKey, fpKey}) colocate on one Redis Cluster slot")
}

// isTypeConvApplyHashtagCall checks whether expr is a method call of the shape
// `KeyNamespace(<arg>).applyHashtag(<keyParam>, "<role>")`. This is the
// type-conversion chain form used in http_idempotency.go, as opposed to the
// struct-field chain form checked by isApplyHashtagCall.
//
// The first argument to applyHashtag MUST be a plain identifier (not a literal)
// to catch regressions where a hardcoded string sneaks into the hashtag.
func isTypeConvApplyHashtagCall(expr ast.Expr, wantRole string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	outer, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || outer.Sel.Name != "applyHashtag" {
		return false
	}
	// The receiver of applyHashtag must be a CallExpr (type conversion or constructor).
	// We accept any CallExpr as the receiver — renaming KeyNamespace to a different
	// type still counts as the sanctioned funnel if it calls applyHashtag.
	if _, ok := outer.X.(*ast.CallExpr); !ok {
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

// TestHTTPIdempotency_MockDispatchSuffixMatch confirms adapters/redis/mock_test.go
// (or the HTTP-specific test file, if separate) recognizes the HTTP idempotency
// keys via suffix matching (":resp" / ":lease"), not legacy prefix matching.
// This is a sibling check to TestIdempotency_MockDispatchSuffixMatch for the
// HTTP store's distinct ":resp" suffix (in addition to ":lease").
func TestHTTPIdempotency_MockDispatchSuffixMatch(t *testing.T) {
	root := findModuleRoot(t)
	// The HTTP idempotency mock dispatch may live in the same mock_test.go or
	// in a dedicated http_idempotency_test.go. Scan both if present.
	candidateFiles := []string{
		filepath.Join(root, "adapters", "redis", "mock_test.go"),
		filepath.Join(root, "adapters", "redis", "http_idempotency_test.go"),
	}

	hasSuffixResp := false
	hasSuffixLease := false

	fset := token.NewFileSet()
	for _, path := range candidateFiles {
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			// File may not exist (http_idempotency_test.go is optional); skip.
			continue
		}
		scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "strings" {
				return
			}
			if sel.Sel.Name != "HasSuffix" || len(call.Args) != 2 {
				return
			}
			arg, ok := stringLit(call.Args[1])
			if !ok {
				return
			}
			if arg == ":resp" {
				hasSuffixResp = true
			}
			if arg == ":lease" {
				hasSuffixLease = true
			}
		})
	}

	assert.True(t, hasSuffixResp,
		"adapters/redis mock/test files must dispatch HTTP idempotency Lua scripts via "+
			"strings.HasSuffix(_, \":resp\") to match the cluster-safe key naming "+
			"(IDEMPOTENCY-LUA-HASHTAG-01)")
	assert.True(t, hasSuffixLease,
		"adapters/redis mock/test files must dispatch HTTP idempotency release via "+
			"strings.HasSuffix(_, \":lease\") to match the cluster-safe key naming "+
			"(IDEMPOTENCY-LUA-HASHTAG-01)")
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

	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "strings" {
			return
		}
		if len(call.Args) != 2 {
			return
		}
		arg, ok := stringLit(call.Args[1])
		if !ok {
			return
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
