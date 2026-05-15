package archtest

// sessionrefresh_stale_epoch_reject_test.go — Hard guard that
// sessionrefresh.refreshInTx contains the S4d stale-epoch reject branch.
//
// INVARIANT: SESSIONREFRESH-STALE-EPOCH-REJECT-01
//
// AI-rebust grade: Hard for the AST shape (presence of a `!=` BinaryExpr
// whose operands include AuthzEpochAtIssue + AuthzEpoch, immediately
// followed by a cascade entry that includes a `stale-epoch` literal stage
// marker). The combination of three independent anchors makes accidental
// omission detectable.
//
// Rule: the function body of `(*Service).refreshInTx` in
// cells/accesscore/slices/sessionrefresh/service.go MUST:
//
//  1. Contain a SelectorExpr referencing `AuthzEpochAtIssue` — the row
//     provenance field introduced by ADR §A8.
//  2. Contain a BinaryExpr Op==NEQ comparing AuthzEpochAtIssue against
//     AuthzEpoch (one side may be reordered) — the strict inequality keeps
//     the check fail-closed (`> ` would let future-epoch tokens through).
//  3. Contain a string literal "stale-epoch" — the cascade stage marker
//     that proves the reject branch routes into the unified reuse cascade
//     (handleReuseDetected) instead of a bare 401.
//
// Blind-spot disclosure:
//   - Helper extraction: if the epoch check moves into a helper function
//     called from refreshInTx, prong 1+2 miss the BinaryExpr in the outer
//     body. Prong 3 ("stale-epoch" literal) anchors the stage marker and
//     would still surface the call. The companion blind-spot test asserts
//     no unexported helper named like *epoch*/*stale* is called from
//     refreshInTx, keeping the body scan sufficient.
//   - Operator drift: prong 2 only accepts `!=`. If the operator becomes `>`
//     or `<`, the rule fires. The RED fixture proves this.

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
	staleEpochServiceFile   = "cells/accesscore/slices/sessionrefresh/service.go"
	staleEpochFuncName      = "refreshInTx"
	staleEpochRowFieldName  = "AuthzEpochAtIssue"
	staleEpochUserFieldName = "AuthzEpoch"
	staleEpochStageMarker   = "stale-epoch"
	staleEpochRedFixtureRel = "tools/archtest/testdata/sessionrefresh_stale_epoch_red/service.go"
)

// TestSessionrefreshStaleEpochReject_01 (production-side).
func TestSessionrefreshStaleEpochReject_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, staleEpochServiceFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "SESSIONREFRESH-STALE-EPOCH-REJECT-01: parse %s", staleEpochServiceFile)

	body := findFuncBody(file, staleEpochFuncName)
	require.NotNilf(t, body,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01: function %q not found in %s",
		staleEpochFuncName, staleEpochServiceFile)

	hasRowField := bodyContainsSelectorName(body, staleEpochRowFieldName)
	hasUserField := bodyContainsSelectorName(body, staleEpochUserFieldName)
	hasInequality := bodyContainsAuthzEpochAtIssueNotEqual(body)
	hasStaleMarker := bodyContainsStringLiteral(body, staleEpochStageMarker)

	assert.Truef(t, hasRowField,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01: %q in %s must reference %q (row provenance)",
		staleEpochFuncName, staleEpochServiceFile, staleEpochRowFieldName)
	assert.Truef(t, hasUserField,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01: %q in %s must reference %q (live user epoch)",
		staleEpochFuncName, staleEpochServiceFile, staleEpochUserFieldName)
	assert.Truef(t, hasInequality,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01: %q in %s must contain a strict inequality (`!=`) "+
			"between %q and %q — operator must be fail-closed (`>`/`<` regress to false-pass)",
		staleEpochFuncName, staleEpochServiceFile,
		staleEpochRowFieldName, staleEpochUserFieldName)
	assert.Truef(t, hasStaleMarker,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01: %q in %s must contain the %q stage literal — "+
			"this is the cascade stage marker that routes through handleReuseDetected; "+
			"a bare 401 without this literal indicates the stale-grant cascade was bypassed",
		staleEpochFuncName, staleEpochServiceFile, staleEpochStageMarker)
}

// TestSessionrefreshStaleEpochReject_RedFixtureDetected: the RED fixture
// removes the inequality and stage marker; our detectors must reject it.
func TestSessionrefreshStaleEpochReject_RedFixtureDetected(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, staleEpochRedFixtureRel)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "parse RED fixture %s", staleEpochRedFixtureRel)

	body := findFuncBody(file, staleEpochFuncName)
	require.NotNilf(t, body, "RED fixture must contain %q", staleEpochFuncName)

	// Fixture is intentionally missing the inequality + marker.
	assert.False(t, bodyContainsAuthzEpochAtIssueNotEqual(body),
		"RED fixture self-check: fixture must NOT contain the row != user inequality")
	assert.False(t, bodyContainsStringLiteral(body, staleEpochStageMarker),
		"RED fixture self-check: fixture must NOT contain the %q literal", staleEpochStageMarker)
}

// bodyContainsAuthzEpochAtIssueNotEqual reports whether the body contains a
// BinaryExpr Op==NEQ where one side references AuthzEpochAtIssue and the
// other references AuthzEpoch.
func bodyContainsAuthzEpochAtIssueNotEqual(body *ast.BlockStmt) bool {
	found := false
	scanner.EachInSubtree[ast.BinaryExpr](body, func(be *ast.BinaryExpr) {
		if found {
			return
		}
		if be.Op != gotoken.NEQ {
			return
		}
		hasRow := exprContainsSelectorName(be.X, staleEpochRowFieldName) ||
			exprContainsSelectorName(be.Y, staleEpochRowFieldName)
		hasUser := exprContainsSelectorName(be.X, staleEpochUserFieldName) ||
			exprContainsSelectorName(be.Y, staleEpochUserFieldName)
		if hasRow && hasUser {
			found = true
		}
	})
	return found
}

// bodyContainsStringLiteral reports whether body contains *ast.BasicLit
// STRING with the given (unquoted) value.
func bodyContainsStringLiteral(body *ast.BlockStmt, want string) bool {
	found := false
	scanner.EachInSubtree[ast.BasicLit](body, func(bl *ast.BasicLit) {
		if bl.Kind == gotoken.STRING && stripBackticksOrQuotes(bl.Value) == want {
			found = true
		}
	})
	return found
}

// Silence unused import.
var _ = strings.HasPrefix
