// INVARIANT: CHANGEPASSWORD-INACTIVE-GATE-01
//
// Package archtest enforces CHANGEPASSWORD-INACTIVE-GATE-01: the
// identitymanage slice's changePasswordInTx MUST call
// credentialauthority.Assert BEFORE the UpdatePassword credential mutation.
// A suspended/locked account must never have its password rewritten; the
// inactive gate has to fail-closed before the write, not after the commit
// (issue #1017 — the gate previously ran post-commit inside IssueForUser,
// so the password was already durable when the 403 was returned).
//
// Why this rule exists (and is NOT folded into
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01's upstream prong): identitymanage is
// deliberately NOT in that funnel's sliceFunnelScopes, because
// changePasswordInTx legitimately reads user.PasswordVersion for the CAS
// write — an orthogonal concern to the credential-pin the funnel governs.
// Adding identitymanage to sliceFunnelScopes would false-positive on that
// CAS read. This rule therefore independently guards the gate's PLACEMENT
// (presence + ordering) for the one path that needs it, instead of forcing
// all of identitymanage's field reads through Assert.
//
// AI-robust: Medium. The security-critical callee (credentialauthority.Assert)
// is type-resolved via archtest.ResolvePackageRef to its exact *types.Func
// identity (pkgPath+name, not a string name anchor), so an unrelated `Assert`
// method does not satisfy the gate. Ordering uses statement-level
// dominance-lite: the Assert must be an UNCONDITIONAL top-level statement
// (ExprStmt/AssignStmt expr, IfStmt.Init guard, or ReturnStmt result) ordered
// before the first top-level statement containing the mutation — a token.Pos
// comparison alone (the pre-#1017-F2 form) is insufficient because a
// conditional `if cond { Assert(...) }` is textually earlier yet does not
// dominate the mutation. Medium (not Hard) because (a) a cross-function helper
// extraction is a residual escape and (b) dominance-lite is not full control-
// flow dominance (see blind-spot inventory). Full CFG/SSA dominance — the path
// to Hard — is tracked in #1212; a type-state token proving Assert ran is
// over-engineering for a single call site, so Medium is the documented ceiling.
//
// Blind-spot inventory (tools: archtest.RunTyped + archtest.ResolvePackageRef
// + scanner.EachInSubtree[ast.CallExpr]):
//
//   - Cross-function extraction: if the Assert call is moved into a helper
//     `assertActive(user)` called from changePasswordInTx, the CallExpr
//     resolving to credentialauthority.Assert no longer appears directly in
//     the function body and this rule reports "missing gate". That is a
//     FALSE POSITIVE that forces the author back to the inline form — the
//     fail-closed direction — so it is acceptable. The inverse (a helper
//     that hides an after-mutation gate) cannot pass either, because the
//     direct Assert CallExpr would be absent. The residual true escape is
//     only if BOTH the gate AND the mutation move into the same helper in
//     the correct order; that helper would itself need the same review.
//
//   - Mutation anchor by selector name: the UpdatePassword anchor is matched
//     by selector name (sel.Sel.Name == "UpdatePassword"), not type-resolved,
//     because it is only a positional marker for "the credential write" within
//     this one known function. Renaming the repo method requires updating
//     cpgMutationMethod here; the rule fails loudly ("no mutation anchor") if
//     the name disappears, so it cannot silently no-op. If UpdatePassword is
//     moved into a method-value capture (`fn := s.repo.UpdatePassword; fn(...)`)
//     the anchor's CallExpr.Fun becomes an *ast.Ident and the selector match
//     misses it → the rule reports "missing gate" rather than an ordering
//     violation. That is still fail-closed (it forces the author back to the
//     inline form), so the capture form is not a silent escape.
//
//   - Method-value capture (`fn := credentialauthority.Assert; fn(user)`):
//     the deferred fn() CallExpr's Fun is an *ast.Ident, which
//     ResolvePackageRef on call.Fun would not resolve to Assert, so the gate
//     would read as absent → "missing gate" diagnostic (fail-closed). The
//     reverse-self-check below asserts no such capture appears in the target
//     function.
//
//   - Dominance-lite, not full CFG (#1212): the unconditional-slot check
//     (ExprStmt/AssignStmt/IfStmt.Init/ReturnStmt) + statement-index ordering
//     approximates "Assert dominates the mutation" for a linear function body.
//     It does NOT model switch/select cases, early-return/goto, or labeled
//     control flow. The known directions are fail-closed (a non-top-level or
//     out-of-order gate is rejected). Full x/tools/go/cfg or go/ssa dominator
//     verification is the path to Hard, tracked in #1212.
//
// Self-check: TestChangePasswordInactiveGate_01_NegativeFixture loads four
// testdata packages (green / red_no_gate / red_gate_after_mutation /
// red_conditional_gate_bypass) via archtest.RunTyped, sharing the same
// changePasswordGateDiagnostics core as the production scan. Fixtures live
// under cells/accesscore/ (not tools/archtest/testdata/) because they import
// the internal credentialauthority package.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

const (
	ruleChangePasswordInactiveGate01 = "CHANGEPASSWORD-INACTIVE-GATE-01"
	// cpgTargetFunc is the function whose gate placement is governed. It is a
	// method on *Service in production; the detector matches by name and is
	// receiver-agnostic.
	cpgTargetFunc = "changePasswordInTx"
	// cpgMutationMethod is the selector-name marker for the credential write.
	cpgMutationMethod = "UpdatePassword"
)

// TestChangePasswordInactiveGate_01 enforces that the production
// changePasswordInTx in the identitymanage slice calls
// credentialauthority.Assert before UpdatePassword.
//
// credAuthorityPkgPath / credAuthorityFnName are shared with
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (same archtest package) — single
// source for the funnel callee identity, so the two rules cannot drift on
// which symbol is the credential-authority gate.
func TestChangePasswordInactiveGate_01(t *testing.T) {
	t.Parallel()

	var sawTarget bool
	diags := Run(t, Typed(TypedOpts{}, []string{
		"./cells/accesscore/slices/identitymanage/...",
	}),

		func(p *Pass) []Diagnostic {
			if !p.Typed() || p.Fset == nil {
				return nil
			}
			var d []Diagnostic
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				fileDiags, found := changePasswordGateDiagnostics(p.TypesInfo, p.Fset, file, rel)
				if found {
					sawTarget = true
				}
				d = append(d, fileDiags...)
			}
			return d
		})

	if !sawTarget {
		t.Fatalf("%s: target function %q not found under "+
			"cells/accesscore/slices/identitymanage/ — renamed or removed? "+
			"The inactive-gate ordering check must not be a silent no-op.",
			ruleChangePasswordInactiveGate01, cpgTargetFunc)
	}

	Report(t, ruleChangePasswordInactiveGate01, diags)
}

// TestChangePasswordInactiveGate_01_NegativeFixture verifies the detector
// fires on the RED fixtures (missing gate / gate-after-mutation) and stays
// silent on the GREEN fixture (gate-before-mutation).
func TestChangePasswordInactiveGate_01_NegativeFixture(t *testing.T) {
	t.Parallel()

	const fixtureBase = "./cells/accesscore/slices/identitymanage/testdata/changepassword_inactive_gate"

	cases := []struct {
		subdir         string
		wantViolations bool
	}{
		{subdir: "green", wantViolations: false},
		{subdir: "red_no_gate", wantViolations: true},
		{subdir: "red_gate_after_mutation", wantViolations: true},
		{subdir: "red_conditional_gate_bypass", wantViolations: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.subdir, func(t *testing.T) {
			t.Parallel()

			var sawTarget bool
			diags := Run(t, Typed(TypedOpts{}, []string{fixtureBase + "/" + tc.subdir}),
				func(p *Pass) []Diagnostic {
					if !p.Typed() || p.Fset == nil {
						return nil
					}
					var d []Diagnostic
					for _, file := range p.Files {
						rel := p.Rel(file)
						fileDiags, found := changePasswordGateDiagnostics(p.TypesInfo, p.Fset, file, rel)
						if found {
							sawTarget = true
						}
						d = append(d, fileDiags...)
					}
					return d
				})

			if !sawTarget {
				t.Fatalf("%s: fixture %q has no %s function — fixture is broken",
					ruleChangePasswordInactiveGate01, tc.subdir, cpgTargetFunc)
			}
			if tc.wantViolations && len(diags) == 0 {
				t.Errorf("%s: fixture %q expected ≥1 diagnostic, got 0 — detector broken",
					ruleChangePasswordInactiveGate01, tc.subdir)
			}
			if !tc.wantViolations && len(diags) > 0 {
				for _, d := range diags {
					t.Errorf("  %s:%d: %s", d.Rel, d.Line, d.Message)
				}
				t.Errorf("%s: fixture %q expected 0 diagnostics, got %d",
					ruleChangePasswordInactiveGate01, tc.subdir, len(diags))
			}
		})
	}
}

// changePasswordGateDiagnostics scans file for a FuncDecl named cpgTargetFunc
// and, if found, checks that credentialauthority.Assert is called before the
// UpdatePassword mutation inside its body. Returns the diagnostics plus whether
// the target function was present (so the caller can fail on a silent no-op).
func changePasswordGateDiagnostics(info *types.Info, fset *token.FileSet, file *ast.File, rel string) (diags []Diagnostic, foundFunc bool) {
	// SCANNER-FRAMEWORK-USAGE-01 Path B compliance: depth-1 typed walk of
	// file.Decls *ast.FuncDecl entries. Original `for _, decl := range
	// file.Decls { decl.(*ast.FuncDecl) }` is the Path B violation.
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Name.Name != cpgTargetFunc || fd.Body == nil {
			return
		}
		foundFunc = true
		diags = append(diags, checkGateOrdering(info, fset, fd, rel)...)
	})
	return diags, foundFunc
}

// checkGateOrdering reports a diagnostic unless changePasswordInTx contains an
// UNCONDITIONAL top-level credentialauthority.Assert guard that precedes the
// UpdatePassword mutation statement.
//
// Dominance-lite (#1017 F2): instead of comparing token.Pos (which a
// conditional `if cond { Assert(...) }` defeats — Assert is textually earlier
// yet does not run on every path), it walks fd.Body.List top-level statements
// and requires the Assert to sit in an UNCONDITIONAL evaluation slot
// (ExprStmt/AssignStmt expression, IfStmt.Init guard, or ReturnStmt result) of a
// statement that is ordered before the first statement containing the mutation.
// An Assert nested only inside a conditional/loop body is rejected. This is not
// full CFG dominance (see godoc blind-spot inventory + the SSA-upgrade issue).
func checkGateOrdering(info *types.Info, fset *token.FileSet, fd *ast.FuncDecl, rel string) []Diagnostic {
	assertIdx, mutationIdx := -1, -1
	var assertNode, mutationNode ast.Node
	for i, stmt := range fd.Body.List {
		if assertIdx == -1 && stmtIsUnconditionalAssert(info, stmt) {
			assertIdx, assertNode = i, stmt
		}
		if mutationIdx == -1 && stmtContainsMutationAnchor(stmt) {
			mutationIdx, mutationNode = i, stmt
		}
	}

	line := func(n ast.Node) int {
		if n == nil {
			return 0
		}
		return fset.Position(n.Pos()).Line
	}

	switch {
	case mutationIdx == -1:
		return []Diagnostic{{Rel: rel, Line: line(fd), Message: fmt.Sprintf(
			"%s: %s contains no top-level %s(...) mutation anchor — function shape "+
				"changed; the inactive-gate dominance check cannot verify the gate "+
				"precedes the password write. Update cpgMutationMethod if the repo "+
				"method was renamed.",
			ruleChangePasswordInactiveGate01, cpgTargetFunc, cpgMutationMethod,
		)}}
	case assertIdx == -1:
		return []Diagnostic{{Rel: rel, Line: line(mutationNode), Message: fmt.Sprintf(
			"%s: %s has no UNCONDITIONAL top-level credentialauthority.Assert guard "+
				"before the %s mutation — a suspended/locked account's password would "+
				"be rewritten. A gate nested inside a conditional does not dominate the "+
				"mutation; place `if err := credentialauthority.Assert(user); err != nil "+
				"{ return ... }` as a top-level statement (issue #1017).",
			ruleChangePasswordInactiveGate01, cpgTargetFunc, cpgMutationMethod,
		)}}
	case assertIdx >= mutationIdx:
		return []Diagnostic{{Rel: rel, Line: line(assertNode), Message: fmt.Sprintf(
			"%s: the credentialauthority.Assert guard does not precede %s in %s — the "+
				"inactive gate must run BEFORE the credential mutation, else the password "+
				"is committed before the 403 (issue #1017 regression).",
			ruleChangePasswordInactiveGate01, cpgMutationMethod, cpgTargetFunc,
		)}}
	default:
		return nil
	}
}

// stmtIsUnconditionalAssert reports whether stmt evaluates a
// credentialauthority.Assert call on every path that reaches it — i.e. the call
// sits in a slot that always runs: an ExprStmt/AssignStmt expression, an
// IfStmt.Init guard (`if err := Assert(...); err != nil`), or a ReturnStmt
// result. An Assert in an IfStmt/ForStmt/etc. BODY is conditional and returns
// false (that is the #1017 F2 bypass the Pos-only detector missed).
func stmtIsUnconditionalAssert(info *types.Info, stmt ast.Stmt) bool {
	for _, slot := range unconditionalSlots(stmt) {
		if nodeContainsAssertCall(info, slot) {
			return true
		}
	}
	return false
}

// unconditionalSlots returns the sub-nodes of stmt that are evaluated whenever
// stmt is reached. Crucially it excludes IfStmt.Body/Else, ForStmt.Body, etc.,
// which run only conditionally.
func unconditionalSlots(stmt ast.Stmt) []ast.Node {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		return []ast.Node{s.X}
	case *ast.AssignStmt:
		out := make([]ast.Node, 0, len(s.Rhs))
		for _, e := range s.Rhs {
			out = append(out, e)
		}
		return out
	case *ast.IfStmt:
		if s.Init != nil {
			return []ast.Node{s.Init} // Init runs unconditionally; Body/Else do not
		}
		return nil
	case *ast.ReturnStmt:
		out := make([]ast.Node, 0, len(s.Results))
		for _, e := range s.Results {
			out = append(out, e)
		}
		return out
	default:
		return nil
	}
}

// nodeContainsAssertCall reports whether n's subtree contains a CallExpr whose
// callee type-resolves to credentialauthority.Assert.
func nodeContainsAssertCall(info *types.Info, n ast.Node) bool {
	_, found := FindFirstInSubtree[ast.CallExpr](n, func(call *ast.CallExpr) bool {
		return IsCallToPkgFunc(info, call, credAuthorityPkgPath, credAuthorityFnName)
	})
	return found
}

// stmtContainsMutationAnchor reports whether stmt's subtree contains a call to
// the UpdatePassword mutation anchor (selector-name match; see godoc). The
// mutation itself may be nested — only the GATE must be unconditional.
func stmtContainsMutationAnchor(stmt ast.Stmt) bool {
	_, found := FindFirstInSubtree[ast.CallExpr](stmt, func(call *ast.CallExpr) bool {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel != nil && sel.Sel.Name == cpgMutationMethod
	})
	return found
}
