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

import (
	"go/ast"
	"go/parser"
	"go/token"
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

// TestSessionvalidateEpochCompare_01 enforces SESSIONVALIDATE-EPOCH-COMPARE-01:
// the enforceSessionState function in sessionvalidate/service.go must contain
// both a reference to AuthzEpoch and a GetByID call. This prevents a future
// refactor from silently removing the epoch-invariant check.
//
// Scanning tool: pure AST (go/parser), no type info required — the invariant
// is expressed entirely in terms of identifier names within the function body.
func TestSessionvalidateEpochCompare_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	targetFile := filepath.Join(root, sessionvalidateServiceFile)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, targetFile, nil, parser.SkipObjectResolution)
	require.NoError(t, err, "parse %s", sessionvalidateServiceFile)

	body := findFuncBody(file, enforceFuncName)
	require.NotNil(t, body,
		"SESSIONVALIDATE-EPOCH-COMPARE-01: function %q not found in %s — "+
			"was the function renamed or removed?",
		enforceFuncName, sessionvalidateServiceFile)

	hasAuthzEpoch := bodyContainsSelectorName(body, "AuthzEpoch")
	hasGetByID := bodyContainsMethodCall(body, "GetByID")

	assert.True(t, hasAuthzEpoch,
		"SESSIONVALIDATE-EPOCH-COMPARE-01: %q in %s must reference 'AuthzEpoch' — "+
			"epoch invariant check (user.AuthzEpoch > claims.AuthzEpoch) must not be removed",
		enforceFuncName, sessionvalidateServiceFile)
	assert.True(t, hasGetByID,
		"SESSIONVALIDATE-EPOCH-COMPARE-01: %q in %s must call a method named 'GetByID' — "+
			"user repo read is required to obtain the server-side epoch for comparison",
		enforceFuncName, sessionvalidateServiceFile)
}

// TestSessionvalidateEpochCompare_RedFixtureMisses verifies the RED fixture
// (testdata/sessionvalidate_no_epoch_compare_red/service.go) does NOT contain
// the AuthzEpoch selector — proving the scanner can distinguish missing from present.
// This is the "反向 RED 自检" (reverse RED self-check).
func TestSessionvalidateEpochCompare_RedFixtureMisses(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixtureFile := filepath.Join(root,
		"tools", "archtest", "testdata", "sessionvalidate_no_epoch_compare_red", "service.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, fixtureFile, nil, parser.SkipObjectResolution)
	require.NoError(t, err, "parse RED fixture")

	body := findFuncBody(file, enforceFuncName)
	require.NotNil(t, body, "RED fixture must contain function %q", enforceFuncName)

	hasAuthzEpoch := bodyContainsSelectorName(body, "AuthzEpoch")
	hasGetByID := bodyContainsMethodCall(body, "GetByID")

	// The RED fixture intentionally omits both — assert both are absent.
	assert.False(t, hasAuthzEpoch,
		"RED fixture self-check: fixture must NOT contain 'AuthzEpoch' — "+
			"if it does, the GREEN scanner cannot distinguish the absent-epoch case")
	assert.False(t, hasGetByID,
		"RED fixture self-check: fixture must NOT contain 'GetByID' — "+
			"if it does, the GREEN scanner cannot distinguish the missing-call case")
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

	fset := token.NewFileSet()
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
