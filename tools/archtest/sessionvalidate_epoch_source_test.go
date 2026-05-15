package archtest

// sessionvalidate_epoch_source_test.go — Hard guard on the SOURCE of the
// epoch comparison inside sessionvalidate.enforceSessionState.
//
// INVARIANT: SESSIONVALIDATE-EPOCH-SOURCE-01
//
// AI-rebust grade: Hard for SoR identity (the comparison must reference the
// row provenance field AuthzEpochAtIssue, not any other token-side field).
//
// Companion to SESSIONVALIDATE-EPOCH-COMPARE-01 (which enforces the `!=`
// operator). Together they form the row-SoR contract: ADR §A8 moves
// credential provenance from the JWT claim to session/refresh rows; the
// validate path must compare against the row, never against a claim.
//
// Rule: the function body of (*Service).enforceSessionState in
// cells/accesscore/slices/sessionvalidate/service.go MUST contain a
// BinaryExpr with Op==NEQ whose two operands together reference:
//   - AuthzEpoch (the live user.authz_epoch read from userRepo)
//   - AuthzEpochAtIssue (the row provenance field on session.ValidateView)
//
// Comparing against any other selector (`claims.AuthzEpoch`, the deleted
// JWT claim field) regresses ADR §A8 and is rejected here.
//
// Blind-spot disclosure:
//   - Field rename to a synonym (e.g. `EpochAtIssue`) would bypass this
//     rule's literal-name match. The companion JWT-CLAIMS-NO-AUTHZ-EPOCH-01
//     prong 1 anchors AuthzEpochAtIssue at the kernel/session.ValidateView
//     definition site, making any rename require coordinated PR changes.
//   - Promoted-field embedding does not change the selector name, so the
//     scan still fires correctly.

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	epochSourceAtIssueField = "AuthzEpochAtIssue"
	epochSourceLiveField    = "AuthzEpoch"
	// Reuse sessionvalidateServiceFile + enforceFuncName from
	// sessionvalidate_epoch_compare_test.go.
)

// TestSessionvalidateEpochSource_01 enforces the row-SoR contract on the
// epoch comparison inside enforceSessionState.
func TestSessionvalidateEpochSource_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, sessionvalidateServiceFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "SESSIONVALIDATE-EPOCH-SOURCE-01: parse %s", sessionvalidateServiceFile)

	body := findFuncBody(file, enforceFuncName)
	require.NotNilf(t, body,
		"SESSIONVALIDATE-EPOCH-SOURCE-01: function %q not found in %s",
		enforceFuncName, sessionvalidateServiceFile)

	if !bodyContainsRowEpochInequality(body) {
		assert.Fail(t,
			"SESSIONVALIDATE-EPOCH-SOURCE-01 violation",
			"function %q in %s must compare %q with %q (row SoR). "+
				"ADR §A8: epoch provenance lives on the session row "+
				"(view.AuthzEpochAtIssue), not in the JWT claim. The compare "+
				"path `user.AuthzEpoch != view.AuthzEpochAtIssue` is the only "+
				"shape that satisfies the contract.",
			enforceFuncName, sessionvalidateServiceFile,
			epochSourceLiveField, epochSourceAtIssueField)
	}
}

// TestSessionvalidateEpochSource_RedFixtureDetected: a fixture that
// compares user.AuthzEpoch against claims.AuthzEpoch (S4b shape) is the
// regression we want to catch. Our detector must reject it.
func TestSessionvalidateEpochSource_RedFixtureDetected(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixtureFile := filepath.Join(root,
		"tools", "archtest", "testdata", "sessionvalidate_no_epoch_compare_red", "service.go")

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, fixtureFile, nil, parser.SkipObjectResolution)
	require.NoError(t, err, "parse RED fixture")

	body := findFuncBody(file, enforceFuncName)
	require.NotNil(t, body, "RED fixture must contain function %q", enforceFuncName)

	// The S4b-era RED fixture uses `>` (covered by EPOCH-COMPARE rule) AND
	// references claims.AuthzEpoch (not AuthzEpochAtIssue) — both forms
	// regress the row-SoR contract. Our detector must reject it.
	assert.False(t, bodyContainsRowEpochInequality(body),
		"RED fixture self-check: fixture must NOT satisfy the row-SoR contract "+
			"(it compares against claims.AuthzEpoch, not view.AuthzEpochAtIssue)")
}

// bodyContainsRowEpochInequality reports whether body contains a BinaryExpr
// with Op==NEQ whose two operands together reference BOTH AuthzEpoch (live
// user epoch) and AuthzEpochAtIssue (row provenance). Order is irrelevant.
func bodyContainsRowEpochInequality(body *ast.BlockStmt) bool {
	found := false
	scanner.EachInSubtree[ast.BinaryExpr](body, func(be *ast.BinaryExpr) {
		if found || be.Op != gotoken.NEQ {
			return
		}
		hasLive := exprContainsSelectorName(be.X, epochSourceLiveField) ||
			exprContainsSelectorName(be.Y, epochSourceLiveField)
		hasAtIssue := exprContainsSelectorName(be.X, epochSourceAtIssueField) ||
			exprContainsSelectorName(be.Y, epochSourceAtIssueField)
		if hasLive && hasAtIssue {
			found = true
		}
	})
	return found
}
