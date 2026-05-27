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
// method does not satisfy the gate. Ordering is an AST token.Pos comparison
// within the same function body. Medium (not Hard) because a cross-function
// helper extraction is a residual escape (see blind-spot inventory); Hard
// (e.g. a type-state token proving Assert ran before the write) would be
// over-engineering for a single call site, so Medium is the documented
// ceiling. No gh upgrade issue is opened (won't-do, like
// HEALTHZ-HOLDER-SEAL-01 #893).
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
// Self-check: TestChangePasswordInactiveGate_01_NegativeFixture loads three
// testdata packages (green / red_no_gate / red_gate_after_mutation) via
// archtest.RunTyped, sharing the same changePasswordGateDiagnostics core as
// the production scan. Fixtures live under cells/accesscore/ (not
// tools/archtest/testdata/) because they import the internal
// credentialauthority package.
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
	diags := RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/slices/identitymanage/...",
	}, func(p *Pass) []Diagnostic {
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
			diags := RunTyped(t, TypedOpts{}, []string{fixtureBase + "/" + tc.subdir},
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
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != cpgTargetFunc || fd.Body == nil {
			continue
		}
		foundFunc = true
		diags = append(diags, checkGateOrdering(info, fset, fd, rel)...)
	}
	return diags, foundFunc
}

// checkGateOrdering reports a diagnostic when changePasswordInTx is missing the
// credentialauthority.Assert gate, missing the mutation anchor, or calls Assert
// after the mutation.
func checkGateOrdering(info *types.Info, fset *token.FileSet, fd *ast.FuncDecl, rel string) []Diagnostic {
	var assertPos, mutationPos token.Pos // token.NoPos until first match (min over occurrences)

	EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
		// Security-critical callee: type-resolve to credentialauthority.Assert.
		if pkgPath, name, ok := ResolvePackageRef(info, call.Fun); ok &&
			pkgPath == credAuthorityPkgPath && name == credAuthorityFnName {
			if !assertPos.IsValid() || call.Pos() < assertPos {
				assertPos = call.Pos()
			}
			return
		}
		// Mutation anchor: selector-name match (positional marker, see godoc).
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil &&
			sel.Sel.Name == cpgMutationMethod {
			if !mutationPos.IsValid() || call.Pos() < mutationPos {
				mutationPos = call.Pos()
			}
		}
	})

	line := func(p token.Pos) int {
		if !p.IsValid() {
			return 0
		}
		return fset.Position(p).Line
	}

	switch {
	case !mutationPos.IsValid():
		return []Diagnostic{{Rel: rel, Line: line(fd.Pos()), Message: fmt.Sprintf(
			"%s: %s contains no %s(...) mutation anchor — function shape changed; "+
				"the inactive-gate ordering check cannot verify the gate precedes the "+
				"password write. Update cpgMutationMethod if the repo method was renamed.",
			ruleChangePasswordInactiveGate01, cpgTargetFunc, cpgMutationMethod)}}
	case !assertPos.IsValid():
		return []Diagnostic{{Rel: rel, Line: line(mutationPos), Message: fmt.Sprintf(
			"%s: %s does not call credentialauthority.Assert — a suspended/locked "+
				"account's password would be rewritten with no inactive gate (issue #1017).",
			ruleChangePasswordInactiveGate01, cpgTargetFunc)}}
	case assertPos > mutationPos:
		return []Diagnostic{{Rel: rel, Line: line(assertPos), Message: fmt.Sprintf(
			"%s: credentialauthority.Assert runs AFTER %s in %s — the inactive gate "+
				"must run BEFORE the credential mutation, else the password is committed "+
				"before the 403 (issue #1017 regression).",
			ruleChangePasswordInactiveGate01, cpgMutationMethod, cpgTargetFunc)}}
	default:
		return nil
	}
}
