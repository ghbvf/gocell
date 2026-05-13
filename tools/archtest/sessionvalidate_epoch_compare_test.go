package archtest

// sessionvalidate_epoch_compare_test.go — AST guard for the epoch-compare
// invariant inside sessionvalidate.enforceSessionState.
//
// INVARIANT: SESSIONVALIDATE-EPOCH-COMPARE-01
//
// AI-rebust grade: Medium (archtest type-aware AST scan).
// Why not Hard: the rule guards a single read-path callsite. "Hard" would require
// decomposing enforceSessionState into a sealed pipeline-of-typed-predicates
// where omitting the epoch check is a compile error — that is not justified by
// current scope (CLAUDE.md §"不预设抽象"). Hard enforcement is a viable follow-up
// once the slice gains multiple callers for enforceSessionState.
//
// Rule: the function body of (sessionvalidate.*).enforceSessionState in
// cells/accesscore/slices/sessionvalidate/service.go must:
//  1. Contain a SelectorExpr referencing claims.AuthzEpoch (or any field named
//     AuthzEpoch, to tolerate minor refactors while keeping the invariant stable).
//  2. Contain a call to a method named GetByID (user repo read to obtain the
//     current server-side epoch for comparison).
//  3. Contain a BinaryExpr with Op == token.NEQ (!=) that references AuthzEpoch
//     on either side — enforcing that the epoch comparison is a strict inequality
//     check and not a weaker operator like > (Finding #2 security fix).
//
// Blind-spot note (ai-collab.md §"工具选定后强制盲区自检"):
// EachInSubtree[ast.FuncDecl] + EachInSubtree[ast.SelectorExpr] covers the
// literal `claims.AuthzEpoch` selector. If the epoch check were refactored
// into a helper function called from enforceSessionState, the body scan would
// miss it (the SelectorExpr moves into the helper's body). This is an
// acceptable Medium trade-off: such a refactor would require a deliberate
// structural change, not an accidental omission. The GREEN baseline test
// (TestSessionvalidateEpochCompare_ServiceFileGreen) catches any such drift
// at review time.
//
// Blind-spot for bodyContainsEpochInequality:
// EachInSubtree[ast.BinaryExpr] covers inline BinaryExpr. If the != check
// were extracted into a helper predicate called from enforceSessionState, the
// body scan would miss the BinaryExpr (it would be in the helper's body).
// This is covered by the existing TestSessionvalidateEpochCompare_BlindSpot_InlinedHelper
// which asserts no epoch-named helper calls exist in enforceSessionState.

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	sessionvalidateServiceFile = "cells/accesscore/slices/sessionvalidate/service.go"
	enforceFuncName            = "enforceSessionState"
)

// exprContainsSelectorName reports whether any SelectorExpr within expr
// has Sel.Name == name. Used by bodyContainsEpochInequality to identify
// which side of a BinaryExpr references AuthzEpoch.
func exprContainsSelectorName(expr ast.Expr, name string) bool {
	found := false
	scanner.EachInSubtree[ast.SelectorExpr](expr, func(sel *ast.SelectorExpr) {
		if sel.Sel != nil && sel.Sel.Name == name {
			found = true
		}
	})
	return found
}

// bodyContainsEpochInequality reports whether the function body contains a
// BinaryExpr with Op == token.NEQ (!=) where at least one side references
// an AuthzEpoch selector. This enforces that the epoch comparison uses strict
// inequality (!=) rather than a weaker operator like > (Finding #2).
func bodyContainsEpochInequality(body *ast.BlockStmt) bool {
	found := false
	scanner.EachInSubtree[ast.BinaryExpr](body, func(be *ast.BinaryExpr) {
		if found {
			return
		}
		if be.Op != gotoken.NEQ {
			return
		}
		// Either side of the != must reference .AuthzEpoch.
		if exprContainsSelectorName(be.X, "AuthzEpoch") || exprContainsSelectorName(be.Y, "AuthzEpoch") {
			found = true
		}
	})
	return found
}

// TestSessionvalidateEpochCompare_01 enforces SESSIONVALIDATE-EPOCH-COMPARE-01:
// the enforceSessionState function in sessionvalidate/service.go must contain:
//  1. A reference to AuthzEpoch (the epoch field exists in the check).
//  2. A GetByID call (user repo is read to fetch the server-side epoch).
//  3. A BinaryExpr with Op == token.NEQ (!=) referencing AuthzEpoch — the epoch
//     comparison must be strict inequality, not a weaker operator like >.
//
// Scanning tool: pure AST (go/parser), no type info required — the invariant
// is expressed entirely in terms of identifier names and operator tokens.
func TestSessionvalidateEpochCompare_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	targetFile := filepath.Join(root, sessionvalidateServiceFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, targetFile, nil, parser.SkipObjectResolution)
	require.NoError(t, err, "parse %s", sessionvalidateServiceFile)

	body := findFuncBody(file, enforceFuncName)
	require.NotNil(t, body,
		"SESSIONVALIDATE-EPOCH-COMPARE-01: function %q not found in %s — "+
			"was the function renamed or removed?",
		enforceFuncName, sessionvalidateServiceFile)

	hasAuthzEpoch := bodyContainsSelectorName(body, "AuthzEpoch")
	hasGetByID := bodyContainsMethodCall(body, "GetByID")
	hasEpochInequality := bodyContainsEpochInequality(body)

	assert.True(t, hasAuthzEpoch,
		"SESSIONVALIDATE-EPOCH-COMPARE-01: %q in %s must reference 'AuthzEpoch' — "+
			"epoch invariant check (user.AuthzEpoch != claims.AuthzEpoch) must not be removed",
		enforceFuncName, sessionvalidateServiceFile)
	assert.True(t, hasGetByID,
		"SESSIONVALIDATE-EPOCH-COMPARE-01: %q in %s must call a method named 'GetByID' — "+
			"user repo read is required to obtain the server-side epoch for comparison",
		enforceFuncName, sessionvalidateServiceFile)
	assert.True(t, hasEpochInequality,
		"SESSIONVALIDATE-EPOCH-COMPARE-01: %q in %s must compare AuthzEpoch with != (token.NEQ) — "+
			"the epoch check must be fail-closed strict inequality, not a weaker operator like >. "+
			"Finding #2: > was changed to != to reject any epoch mismatch including future-epoch tokens",
		enforceFuncName, sessionvalidateServiceFile)
}

// TestSessionvalidateEpochCompare_RedFixtureDetected verifies the RED fixture
// (testdata/sessionvalidate_no_epoch_compare_red/service.go) DOES contain AuthzEpoch
// and GetByID (so those checks pass), but uses > instead of != — proving the
// new bodyContainsEpochInequality check can detect the wrong operator.
// This is the "反向 RED 自检" (reverse RED self-check) for the upgraded rule.
func TestSessionvalidateEpochCompare_RedFixtureDetected(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixtureFile := filepath.Join(root,
		"tools", "archtest", "testdata", "sessionvalidate_no_epoch_compare_red", "service.go")

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, fixtureFile, nil, parser.SkipObjectResolution)
	require.NoError(t, err, "parse RED fixture")

	body := findFuncBody(file, enforceFuncName)
	require.NotNil(t, body, "RED fixture must contain function %q", enforceFuncName)

	hasAuthzEpoch := bodyContainsSelectorName(body, "AuthzEpoch")
	hasGetByID := bodyContainsMethodCall(body, "GetByID")
	hasEpochInequality := bodyContainsEpochInequality(body)

	// The RED fixture has AuthzEpoch and GetByID — those checks pass.
	assert.True(t, hasAuthzEpoch,
		"RED fixture self-check: fixture must contain 'AuthzEpoch' selector (it uses >)")
	assert.True(t, hasGetByID,
		"RED fixture self-check: fixture must contain 'GetByID' call")
	// But the fixture uses > not !=, so the inequality check must FAIL.
	assert.False(t, hasEpochInequality,
		"RED fixture self-check: fixture must NOT satisfy bodyContainsEpochInequality — "+
			"it uses > (GT) not != (NEQ), so the new check must catch it as a violation")
}

// ─── AST helpers ────────────────────────────────────────────────────────

// findFuncBody locates the *ast.BlockStmt body of the first FuncDecl named
// funcName anywhere in the file (includes method declarations with any receiver).
func findFuncBody(file *ast.File, funcName string) *ast.BlockStmt {
	var body *ast.BlockStmt
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if body != nil {
			return // already found
		}
		if fn.Name != nil && fn.Name.Name == funcName && fn.Body != nil {
			body = fn.Body
		}
	})
	return body
}

// bodyContainsSelectorName reports whether any SelectorExpr within body
// has Sel.Name == name (e.g. "AuthzEpoch" matches both `claims.AuthzEpoch`
// and `user.AuthzEpoch`).
func bodyContainsSelectorName(body *ast.BlockStmt, name string) bool {
	found := false
	scanner.EachInSubtree[ast.SelectorExpr](body, func(sel *ast.SelectorExpr) {
		if sel.Sel != nil && sel.Sel.Name == name {
			found = true
		}
	})
	return found
}

// bodyContainsMethodCall reports whether any CallExpr within body has a
// SelectorExpr Fun where Sel.Name == methodName (e.g. "GetByID").
func bodyContainsMethodCall(body *ast.BlockStmt, methodName string) bool {
	found := false
	scanner.EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel != nil && sel.Sel.Name == methodName {
			found = true
		}
	})
	return found
}

// TestSessionvalidateEpochCompare_BlindSpot_InlinedHelper asserts that
// enforceSessionState does NOT delegate the epoch check to an inner helper
// (which would make the body scan a false negative). This test scans for
// calls to unexported functions whose name contains "epoch" or "authz" inside
// enforceSessionState's body — absence proves the body scan is sufficient.
func TestSessionvalidateEpochCompare_BlindSpot_InlinedHelper(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	targetFile := filepath.Join(root, sessionvalidateServiceFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, targetFile, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	body := findFuncBody(file, enforceFuncName)
	if body == nil {
		t.Skip("enforceSessionState not found — skip blind-spot check")
	}

	// Scan for calls to local functions (non-selector CallExpr with Ident Fun)
	// whose name suggests epoch-related logic extracted into a helper.
	var suspiciousCalls []string
	scanner.EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return
		}
		lower := strings.ToLower(ident.Name)
		if strings.Contains(lower, "epoch") || strings.Contains(lower, "authz") {
			pos := fset.Position(call.Pos())
			suspiciousCalls = append(suspiciousCalls,
				"line "+string(rune('0'+pos.Line%10))+": "+ident.Name)
		}
	})

	assert.Empty(t, suspiciousCalls,
		"SESSIONVALIDATE-EPOCH-COMPARE-01 blind-spot: enforceSessionState delegates epoch check to "+
			"a helper function %v — the body scan would miss a refactor that removes the inline check. "+
			"If this is intentional, upgrade the rule to follow the helper.", suspiciousCalls)
}
