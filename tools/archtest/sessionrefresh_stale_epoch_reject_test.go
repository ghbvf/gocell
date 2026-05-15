package archtest

// sessionrefresh_stale_epoch_reject_test.go — guard that locks the
// corrected (post-P2.b) security model for the stale-epoch branch in
// cells/accesscore/slices/sessionrefresh/service.go.
//
// INVARIANT: SESSIONREFRESH-STALE-EPOCH-REJECT-01
//
// AI-rebust grade: Medium.
// Justification: all four prongs use go/parser AST name/string anchors
// (Sel.Name, Ident.Name, BasicLit value) — NOT typeseval type-identity
// resolution. A helper rename breaks the test loudly (BS-1), but a
// same-named re-implementation with different type identity would pass.
// Upgrade path: backlog SESSIONREFRESH-STALE-EPOCH-REJECT-HARDEN-01.
//
// (4 independent anchors, incl. a negative-call assertion):
//
//  1. Operand-wiring anchor: detects the call site in refreshInTx —
//     a CallExpr whose callee Sel.Name == "rejectIfStaleEpoch" with args
//     that carry SelectorExpr "AuthzEpochAtIssue" and SelectorExpr / CallExpr
//     referencing "AuthzEpoch". No other function satisfies both selector
//     conditions simultaneously; form uniqueness means picking any other shape
//     fails the test.
//
//  2. Fail-closed-comparison anchor: the rejectIfStaleEpoch body must contain
//     a BinaryExpr Op==EQL (the `rowEpoch == userEpoch` guard) and must NOT
//     contain a BinaryExpr Op==NEQ, Op==GTR, or Op==LSS (operator drift would
//     convert the fail-closed pass-on-equality encoding into a security hole).
//     Note: `==`-with-early-return is the fail-closed encoding here because the
//     function passes only when the epochs are exactly equal, and rejects on any
//     inequality — equivalent semantics to the old `!=`-reject, expressed as
//     a positive pass-guard instead of a negative reject-guard.
//
//  3. Session-scoped-revoke anchor: the rejectIfStaleEpoch body must contain
//     the string literal "stale-epoch" AND a method call whose Sel.Name ==
//     "cascadeRevoke". Both must be present to prove the correct revocation path
//     (session-scoped only, not user-wide).
//
//  4. Negative-call anchor (core P2.b regression guard): the rejectIfStaleEpoch
//     body must NOT call "handleReuseDetected" and must NOT reference the Ident
//     "CredentialEventRefreshReuse". This is the hard invariant proving stale-epoch
//     is never conflated with the reuse-attack cascade. Also asserts that no
//     residual handleReuseDetected call inside refreshInTx passes a "stale-epoch"
//     literal — closing the bypass via direct inlining.
//
// Rule: the function body of `(*Service).rejectIfStaleEpoch` in
// cells/accesscore/slices/sessionrefresh/service.go MUST:
//
//  1. Be called from `refreshInTx` with args containing SelectorExpr
//     "AuthzEpochAtIssue" and a CallExpr/SelectorExpr referencing "AuthzEpoch".
//  2. Contain a BinaryExpr Op==EQL (the `==` equality guard encoding fail-closed
//     pass-on-match) and NOT contain BinaryExpr Op==NEQ/GTR/LSS (wrong operator
//     would widen the accept window).
//  3. Contain the string literal "stale-epoch" AND a call to "cascadeRevoke"
//     (session-scoped revoke, NOT user-wide invalidation).
//  4. NOT call "handleReuseDetected" and NOT reference "CredentialEventRefreshReuse"
//     (stale-epoch != reuse attack; conflation emits a false security audit event
//     and triggers a redundant user-wide cascade).
//
// Blind-spot disclosure (§盲区自检, mandatory per ai-collab.md):
//
//  BS-1  Helper rename: the anchor for prongs 1, 2, 3, 4 is the function NAME
//        "rejectIfStaleEpoch". If the function is renamed, findFuncBody returns
//        nil and require.NotNil fires loudly — the test fails RED, never silently
//        passes. Same for "refreshInTx" on the call-site scan.
//        Reverse self-check: TestSessionrefreshStaleEpoch_BlindSpot_HelperRename
//        asserts that a deliberately misnamed helper body cannot be found.
//
//  BS-2  Method-value / indirect call of handleReuseDetected: if the code writes
//        `fn := s.handleReuseDetected; fn(...)`, the CallExpr Fun is an Ident
//        not a SelectorExpr — the negative scan (Sel.Name == "handleReuseDetected")
//        would miss it. Documented as accepted risk: the AST anchor catches the
//        direct-call form universally used in this codebase. Production code
//        convention (no method-value aliases for security-critical paths) is
//        separately enforced by code review.
//        Reverse self-check: TestSessionrefreshStaleEpoch_BlindSpot_MethodValueNotCall
//        asserts the production file does NOT contain an Ident named
//        "handleReuseDetected" that is NOT the RHS of a SelectorExpr — i.e.,
//        no bare Ident usage that could be a method-value capture.
//
//  BS-3  Reflection: `reflect.ValueOf(s).MethodByName("handleReuseDetected").Call(...)`
//        is undetectable by pure AST scan. Documented as out-of-scope: reflection
//        in security-critical paths is prohibited by code review policy and would
//        surface in static linters (go vet / staticcheck).

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
	staleEpochServiceFile     = "cells/accesscore/slices/sessionrefresh/service.go"
	staleEpochFuncName        = "refreshInTx"
	staleEpochHelperFuncName  = "rejectIfStaleEpoch"
	staleEpochRowFieldName    = "AuthzEpochAtIssue"
	staleEpochUserFieldName   = "AuthzEpoch"
	staleEpochStageMarker     = "stale-epoch"
	staleEpochCascadeCallee   = "cascadeRevoke"
	staleEpochReuseCallee     = "handleReuseDetected"
	staleEpochReuseEventIdent = "CredentialEventRefreshReuse"
	staleEpochRedFixtureRel   = "tools/archtest/testdata/sessionrefresh_stale_epoch_red/service.go"
)

// TestSessionrefreshStaleEpochReject_01 (production-side): 4-prong Hard guard
// over the post-P2.b stale-epoch security model.
func TestSessionrefreshStaleEpochReject_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, staleEpochServiceFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "SESSIONREFRESH-STALE-EPOCH-REJECT-01: parse %s", staleEpochServiceFile)

	// ── Prong 1: refreshInTx calls rejectIfStaleEpoch with the two epoch operands ──

	refreshBody := findFuncBody(file, staleEpochFuncName)
	require.NotNilf(t, refreshBody,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01: function %q not found in %s",
		staleEpochFuncName, staleEpochServiceFile)

	callsHelper, helperCallHasRowField, helperCallHasUserField := bodyContainsHelperCallWithEpochArgs(
		refreshBody, staleEpochHelperFuncName,
		staleEpochRowFieldName, staleEpochUserFieldName,
	)

	assert.Truef(t, callsHelper,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 1): %q in %s must call %q — "+
			"stale-epoch decision must be delegated to the helper, not inlined",
		staleEpochFuncName, staleEpochServiceFile, staleEpochHelperFuncName)
	assert.Truef(t, helperCallHasRowField,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 1): the %q call in %q must pass %q (row provenance) as an argument",
		staleEpochHelperFuncName, staleEpochFuncName, staleEpochRowFieldName)
	assert.Truef(t, helperCallHasUserField,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 1): the %q call in %q must pass %q (live user epoch) as an argument",
		staleEpochHelperFuncName, staleEpochFuncName, staleEpochUserFieldName)

	// ── Prong 2 + 3 + 4: rejectIfStaleEpoch body invariants ──

	helperBody := findFuncBody(file, staleEpochHelperFuncName)
	require.NotNilf(t, helperBody,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01: function %q not found in %s",
		staleEpochHelperFuncName, staleEpochServiceFile)

	// Prong 2: fail-closed comparison form
	hasEqualGuard := bodyContainsBinaryOp(helperBody, gotoken.EQL)
	hasWrongOp := bodyContainsEpochBinaryOp(helperBody, gotoken.NEQ, gotoken.GTR, gotoken.LSS)

	assert.Truef(t, hasEqualGuard,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 2): %q body must contain BinaryExpr Op==EQL — "+
			"the fail-closed pass-on-equality guard (`if rowEpoch == userEpoch { return nil }`) "+
			"must be the only comparison; `!=` would invert semantics",
		staleEpochHelperFuncName)
	assert.Falsef(t, hasWrongOp,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 2): %q body must NOT contain BinaryExpr Op==NEQ/GTR/LSS — "+
			"wrong operator would widen the accept window (> passes stale-future tokens, != inverts the guard)",
		staleEpochHelperFuncName)

	// Prong 3: session-scoped revoke + stage marker
	hasStaleMarker := bodyContainsStringLiteral(helperBody, staleEpochStageMarker)
	hasCascadeRevoke := staleEpochBodyContainsMethodCall(helperBody, staleEpochCascadeCallee)

	assert.Truef(t, hasStaleMarker,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 3): %q body must contain string literal %q — "+
			"stage marker identifies the revocation reason in audit logs",
		staleEpochHelperFuncName, staleEpochStageMarker)
	assert.Truef(t, hasCascadeRevoke,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 3): %q body must call %q — "+
			"session-scoped revoke is required; bare 401 without revoke leaks the session",
		staleEpochHelperFuncName, staleEpochCascadeCallee)

	// Prong 4: NEGATIVE — must NOT call handleReuseDetected or reference CredentialEventRefreshReuse
	helperCallsReuseHandler := staleEpochBodyContainsMethodCall(helperBody, staleEpochReuseCallee)
	helperRefersReuseEvent := staleEpochBodyContainsIdent(helperBody, staleEpochReuseEventIdent)

	assert.Falsef(t, helperCallsReuseHandler,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 4 NEGATIVE): %q must NOT call %q — "+
			"stale-epoch is NOT a reuse attack; calling handleReuseDetected would emit "+
			"a false CredentialEventRefreshReuse audit event and trigger a redundant "+
			"user-wide cascade (P2.b security model correction)",
		staleEpochHelperFuncName, staleEpochReuseCallee)
	assert.Falsef(t, helperRefersReuseEvent,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 4 NEGATIVE): %q must NOT reference %q — "+
			"stale-epoch path must never emit the reuse-attack event",
		staleEpochHelperFuncName, staleEpochReuseEventIdent)

	// Prong 4 addendum: refreshInTx must not have a residual stale-epoch path
	// that routes directly to handleReuseDetected (bypass via inlining).
	refreshBodyCallsReuseWithStaleMarker := bodyContainsReuseCallWithStaleMarker(refreshBody,
		staleEpochReuseCallee, staleEpochStageMarker)
	assert.Falsef(t, refreshBodyCallsReuseWithStaleMarker,
		"SESSIONREFRESH-STALE-EPOCH-REJECT-01 (prong 4 NEGATIVE addendum): %q must NOT contain "+
			"a %q call that also passes a %q literal — no residual inlined stale-epoch→reuse path",
		staleEpochFuncName, staleEpochReuseCallee, staleEpochStageMarker)
}

// TestSessionrefreshStaleEpochReject_RedFixtureDetected: the RED fixture
// represents the REGRESSED model (rejectIfStaleEpoch calls handleReuseDetected
// and references CredentialEventRefreshReuse, lacks cascadeRevoke). Our detectors
// must fire at least 1 violation.
func TestSessionrefreshStaleEpochReject_RedFixtureDetected(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, staleEpochRedFixtureRel)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "parse RED fixture %s", staleEpochRedFixtureRel)

	refreshBody := findFuncBody(file, staleEpochFuncName)
	require.NotNilf(t, refreshBody, "RED fixture must contain %q", staleEpochFuncName)

	helperBody := findFuncBody(file, staleEpochHelperFuncName)
	require.NotNilf(t, helperBody, "RED fixture must contain %q", staleEpochHelperFuncName)

	// The RED fixture's rejectIfStaleEpoch calls handleReuseDetected —
	// prong 4 negative detectors must fire.
	helperCallsReuseHandler := staleEpochBodyContainsMethodCall(helperBody, staleEpochReuseCallee)
	assert.Truef(t, helperCallsReuseHandler,
		"RED fixture self-check: %q must call %q (regression shape) so prong 4 fires",
		staleEpochHelperFuncName, staleEpochReuseCallee)

	helperRefersReuseEvent := staleEpochBodyContainsIdent(helperBody, staleEpochReuseEventIdent)
	assert.Truef(t, helperRefersReuseEvent,
		"RED fixture self-check: %q must reference %q so prong 4 fires",
		staleEpochHelperFuncName, staleEpochReuseEventIdent)

	// The RED fixture's rejectIfStaleEpoch must NOT call cascadeRevoke —
	// prong 3 must fire (absence of session-scoped revoke).
	hasCascadeRevoke := staleEpochBodyContainsMethodCall(helperBody, staleEpochCascadeCallee)
	assert.Falsef(t, hasCascadeRevoke,
		"RED fixture self-check: %q must NOT call %q (missing session revoke) so prong 3 fires",
		staleEpochHelperFuncName, staleEpochCascadeCallee)
}

// TestSessionrefreshStaleEpoch_BlindSpot_HelperRename (BS-1 reverse self-check):
// asserts that findFuncBody returns nil for a deliberately wrong function name,
// proving the NAME anchor fails loudly on rename rather than silently passing.
func TestSessionrefreshStaleEpoch_BlindSpot_HelperRename(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, staleEpochServiceFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	// A plausible rename that could fool a comment-based check.
	renamed := findFuncBody(file, "rejectStaleEpoch")
	assert.Nil(t, renamed,
		"BS-1: findFuncBody must return nil for a renamed helper — "+
			"proves that if the real function is renamed the prong fires RED, not silently GREEN")
}

// TestSessionrefreshStaleEpoch_BlindSpot_MethodValueNotCall (BS-2 reverse self-check):
// asserts that the production file does NOT contain a bare Ident "handleReuseDetected"
// that is NOT the Sel field of a SelectorExpr — i.e., no method-value capture
// that would escape the Sel.Name negative scan.
func TestSessionrefreshStaleEpoch_BlindSpot_MethodValueNotCall(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	target := filepath.Join(root, staleEpochServiceFile)

	fset := gotoken.NewFileSet()
	file, err := parser.ParseFile(fset, target, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	hasBareIdent := staleEpochFileContainsBareIdent(file, staleEpochReuseCallee)
	assert.Falsef(t, hasBareIdent,
		"BS-2: production file must NOT contain a bare Ident %q outside a SelectorExpr.Sel — "+
			"presence would indicate a method-value capture that escapes the negative-call scan",
		staleEpochReuseCallee)
}

// ─── AST helpers specific to this test file ─────────────────────────────────

// bodyContainsHelperCallWithEpochArgs reports whether body contains a CallExpr
// whose callee Sel.Name == helperName, and for the matching call, whether any
// part of the argument expressions contains a SelectorExpr for rowField and
// userField respectively. Uses scanner.EachInSubtree over each argument to
// satisfy SCANNER-FRAMEWORK-USAGE-01 (no for-range over []ast.Expr + type-assert).
// Returns (callFound, rowFieldInArgs, userFieldInArgs).
func bodyContainsHelperCallWithEpochArgs(body *ast.BlockStmt, helperName, rowField, userField string) (bool, bool, bool) {
	var callFound, hasRow, hasUser bool
	scanner.EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != helperName {
			return
		}
		callFound = true
		// Scan each argument's subtree for the epoch selector names.
		// exprContainsSelectorName internally uses scanner.EachInSubtree,
		// satisfying the framework constraint against for-range + type-assert.
		scanner.EachInSubtree[ast.SelectorExpr](call, func(argSel *ast.SelectorExpr) {
			if argSel == sel {
				return // skip the callee selector itself
			}
			if argSel.Sel != nil {
				if argSel.Sel.Name == rowField {
					hasRow = true
				}
				if argSel.Sel.Name == userField {
					hasUser = true
				}
			}
		})
	})
	return callFound, hasRow, hasUser
}

// bodyContainsBinaryOp reports whether body contains any BinaryExpr with the
// given operator token.
func bodyContainsBinaryOp(body *ast.BlockStmt, op gotoken.Token) bool {
	found := false
	scanner.EachInSubtree[ast.BinaryExpr](body, func(be *ast.BinaryExpr) {
		if be.Op == op {
			found = true
		}
	})
	return found
}

// bodyContainsEpochBinaryOp reports whether body contains a BinaryExpr with
// any of the given operator tokens where at least one side references an epoch
// identifier (the parameter names used in rejectIfStaleEpoch: rowEpoch or
// userEpoch). This excludes ubiquitous `err != nil` guards.
func bodyContainsEpochBinaryOp(body *ast.BlockStmt, ops ...gotoken.Token) bool {
	const epochParam1 = "rowEpoch"
	const epochParam2 = "userEpoch"
	found := false
	scanner.EachInSubtree[ast.BinaryExpr](body, func(be *ast.BinaryExpr) {
		if found {
			return
		}
		hasOp := false
		for _, op := range ops {
			if be.Op == op {
				hasOp = true
				break
			}
		}
		if !hasOp {
			return
		}
		// Only flag when an epoch param appears on either side.
		hasEpoch := staleEpochExprContainsIdent(be.X, epochParam1) ||
			staleEpochExprContainsIdent(be.X, epochParam2) ||
			staleEpochExprContainsIdent(be.Y, epochParam1) ||
			staleEpochExprContainsIdent(be.Y, epochParam2)
		if hasEpoch {
			found = true
		}
	})
	return found
}

// staleEpochExprContainsIdent reports whether expr contains an Ident with the
// given name. Used to narrow binary-op checks to epoch parameters only.
func staleEpochExprContainsIdent(expr ast.Expr, name string) bool {
	found := false
	scanner.EachInSubtree[ast.Ident](expr, func(id *ast.Ident) {
		if id.Name == name {
			found = true
		}
	})
	return found
}

// staleEpochBodyContainsMethodCall reports whether body contains a CallExpr
// where Fun is a SelectorExpr with Sel.Name == methodName.
func staleEpochBodyContainsMethodCall(body *ast.BlockStmt, methodName string) bool {
	found := false
	scanner.EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel != nil && sel.Sel.Name == methodName {
			found = true
		}
	})
	return found
}

// staleEpochBodyContainsIdent reports whether body contains any *ast.Ident
// with Name == name (covers both qualified references like pkg.Ident and bare
// Ident usage).
func staleEpochBodyContainsIdent(body *ast.BlockStmt, name string) bool {
	found := false
	scanner.EachInSubtree[ast.Ident](body, func(id *ast.Ident) {
		if id.Name == name {
			found = true
		}
	})
	return found
}

// bodyContainsReuseCallWithStaleMarker reports whether body contains a CallExpr
// to reuseCallee where any argument of the call is the staleMarker string
// literal. This detects the bypass pattern:
// `handleReuseDetected(ctx, id, sid, "stale-epoch")`.
// Uses scanner.EachInSubtree over the CallExpr to satisfy
// SCANNER-FRAMEWORK-USAGE-01 (no for-range over []ast.Expr + type-assert).
func bodyContainsReuseCallWithStaleMarker(body *ast.BlockStmt, reuseCallee, staleMarker string) bool {
	found := false
	scanner.EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != reuseCallee {
			return
		}
		// Scan the entire CallExpr subtree for a BasicLit string matching
		// the stale marker. The callee selector itself contains no string
		// literals, so all hits are from the argument expressions.
		scanner.EachInSubtree[ast.BasicLit](call, func(bl *ast.BasicLit) {
			if bl.Kind == gotoken.STRING && stripBackticksOrQuotes(bl.Value) == staleMarker {
				found = true
			}
		})
	})
	return found
}

// staleEpochFileContainsBareIdent reports whether any function BODY in the file
// contains an *ast.Ident with Name == name that is NOT the Sel field of a
// SelectorExpr. This detects method-value captures like
// `fn := s.handleReuseDetected` inside function bodies. FuncDecl names are
// excluded because a function named "handleReuseDetected" legitimately appears
// as its own declaration.
func staleEpochFileContainsBareIdent(file *ast.File, name string) bool {
	// Collect all SelectorExpr.Sel positions within function bodies — "not bare".
	selPos := map[gotoken.Pos]bool{}
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		scanner.EachInSubtree[ast.SelectorExpr](fn.Body, func(sel *ast.SelectorExpr) {
			if sel.Sel != nil {
				selPos[sel.Sel.Pos()] = true
			}
		})
	})

	found := false
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		scanner.EachInSubtree[ast.Ident](fn.Body, func(id *ast.Ident) {
			if id.Name == name && !selPos[id.Pos()] {
				found = true
			}
		})
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
