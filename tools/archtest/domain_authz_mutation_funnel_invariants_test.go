package archtest

// domain_authz_mutation_funnel_invariants_test.go — two invariants completing
// the S4e authz-mutation funnel closure.
//
// INVARIANT: DOMAIN-AUTHZ-FIELD-PRIVATE-01
// INVARIANT: AUTHZ-MUTATION-APPLY-FUNNEL-01
//
// Funnel 双向锁评级 (ai-robust.md §"Funnel 双向锁评级"):
//
//   Downstream Hard (DOMAIN-AUTHZ-FIELD-PRIVATE-01):
//     Cross-package write of domain.User authz fields (status,
//     passwordResetRequired, authzEpoch) is a compile error because the fields
//     are unexported — this is the compile-time privatization Hard guarantee.
//     This archtest is the regression net: it fires before a re-export ever
//     reaches a build. Addition of a new public setter beyond the two
//     sanctioned ones, or a reflection-based write, would be caught here.
//
//   Downstream Hard (AUTHZ-MUTATION-APPLY-FUNNEL-01, Rule a — SetStatus /
//   SetPasswordResetRequired caller set):
//     "Form uniqueness" requires three axes, all type-resolved (no string
//     anchors, no file paths, no package prefixes):
//       1. callee identity via typeseval.ResolveMethodCall → exact *types.Func
//       2. caller identity via typeseval.ResolveEnclosingFunc → exact *types.Func
//          for the enclosing FuncDecl
//       3. form completeness — EachInSubtree[ast.SelectorExpr] visits EVERY
//          selector resolving to the banned method (direct call AND
//          function-value capture: `fn := u.SetStatus`, `return u.SetStatus`,
//          `pass(u.SetStatus)`). info.Selections records a MethodVal
//          selection even when the method value is not immediately invoked.
//     Any reference whose enclosing FuncDecl is outside the narrow callsite
//     allowlist fails archtest in CI with no gray zone. Honest caveat: Go
//     does not prevent the calls at compile time (the methods are exported);
//     enforcement is archtest-bound. This is the highest Hard grade
//     reachable in Go for exported-method caller restriction.
//
//     Issue #732 hardening (this file, 2026-05-27): the prior file-level
//     allowlist (setMutatorAllowlist []string) admitted any new function in
//     adminprovision/provisioner.go or identitymanage/service.go to call the
//     setters silently. callsite-level keying eliminates this "same file /
//     different function" slip path. PR #1196 round-2 upgrade: scanner walks
//     all SelectorExpr (not only CallExpr.Fun), closing the previously
//     expressible "method-value capture" bypass that round-1 had documented
//     as a blind spot — now caught directly by form-complete resolution
//     (mirrors credential_invalidate sibling scanners).
//
//     Defensive package-prefix carve-outs for authzmutate/ and domain/
//     removed: production AST has zero references to these setters in those
//     packages today; if a new helper needs the setter, the author must add
//     a callsite entry (explicit acknowledgement, not silent reuse of a
//     stale package allowance).
//
//   Upstream Medium-by-necessity (caller-set upper-bound):
//     The upstream guarantee — that all live-aggregate authz mutations MUST go
//     through authzmutate.Mutator.Apply — is Medium, not Hard, because:
//     (a) identitymanage/service.go and adminprovision/provisioner.go legitimately
//     call SetPasswordResetRequired at creation time (no live sessions yet);
//     routing through authzmutate would be semantically wrong.
//     (b) sealed interfaces or codegen cannot express "creation-time-only" as a
//     compile-time invariant without redesigning the domain model.
//     The Medium ceiling is an accepted architectural trade-off independent of
//     the archtest allowlist granularity; callsite-level keying within Rule (a)
//     is orthogonal — it tightens the archtest tier without changing the
//     architectural ceiling.
//
// Relationship to CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 (Rule b):
//   The Hard closure of the P1.2 / P1-#1 regression class is Rule (a) above —
//   field privatization + SetStatus/SetPasswordResetRequired funnel. Narrowing
//   the credentialinvalidate.Invalidator.Apply caller set (Rule b) is a
//   secondary tightening implemented in-file by modifying
//   upstreamCallerCallsiteAllowlist in
//   credential_invalidate_funnel_invariants_test.go (also upgraded to
//   callsite-level in this PR).
//
//   The ADR §A10 idealization "{authzmutate, sessionrefresh}" is NOT achievable:
//   Delete and changePasswordInTx in identitymanage need Invalidator.Apply co-tx
//   with another write for atomicity; routing through authzmutate would split
//   their transaction. Similarly rbacassign needs role-row write + revoke in one
//   tx. The actual S4e legitimate caller set is {authzmutate/, identitymanage/,
//   rbacassign/, sessionrefresh/} + the funnel package itself.
//   Wave 3 ADR author: the P1 regression class is closed at Rule (a), not at
//   Rule (b). §A10 should be updated to reflect the actual caller set.
//
// Scanning tool: typeseval.SharedResolver + archtest.ResolveMethodCall (callee
// type-resolved) + archtest.ResolveEnclosingFunc (caller type-resolved) +
// scanner.EachInSubtree[ast.SelectorExpr] (form-complete, walks every
// SelectorExpr — not only CallExpr.Fun) for Rule (a); go/types struct field
// and method set inspection for DOMAIN-AUTHZ-FIELD-PRIVATE-01.
//
// Blind-spot self-check (ai-robust.md §"工具选定后强制盲区自检"):
//
// For AUTHZ-MUTATION-APPLY-FUNNEL-01 — ResolveMethodCall resolves via
// info.Selections; ResolveEnclosingFunc walks file.Decls for *ast.FuncDecl
// containing the reference's position. AST forms NOT covered:
//
//  1. Method expression (qualified): `(*domain.User).SetStatus(u, s, t)`
//     Fun is *ast.SelectorExpr resolving via info.Selections as MethodExpr.
//     ResolveMethodCall explicitly accepts types.MethodExpr — this IS covered.
//     Documented for completeness; no self-check needed.
//
//  2. reflect.Value.MethodByName("SetStatus").Call(...): fully AST-invisible.
//     Captured by:
//     TestDomainAuthzMutation_BlindSpot_ReflectMethodByName (asserts absence).
//
//  3. Dot-import: `import . "...domain"` followed by a bare call. SetStatus is
//     a method, not a package-level function, so dot-import does not affect
//     method calls on a receiver. Not applicable; no self-check needed.
//
//  4. Embedded promotion: `type W struct { *domain.User }; w.SetStatus(...)`
//     resolves via info.Selections to the same *types.Func (promoted method
//     Obj() is the original). This IS covered. Documented for completeness.
//
//  5. Package-level var init / const init bypass: `var _ = func() {
//     u.SetStatus(...); return 0 }()`. ResolveEnclosingFunc returns
//     (nil, false) for any node outside a FuncDecl body. The scan loop treats
//     (nil, false) as an AUTOMATIC violation — package-level init has no
//     allowlistable identity. Captured (positively, i.e. confirming the rule
//     fires) by: TestDomainAuthzMutation_BlindSpot_VarInitCall.
//
//  6. FuncLit-inside-FuncDecl semantic choice (NOT a blind spot):
//     ResolveEnclosingFunc collapses a nested FuncLit's identity to its
//     outermost FuncDecl. Rationale: FuncLit author = FuncDecl author;
//     allowlisting the outer FuncDecl implicitly trusts any FuncLit inside.
//     Documented for completeness; no reverse self-check needed.
//
// Function-value capture forms (`fn := u.SetStatus`, `return u.SetStatus`,
// `pass(u.SetStatus)`) are COVERED — they are SelectorExpr nodes resolving
// to the same *types.Func via info.Selections (MethodVal selection records
// even when not immediately invoked). PR #1196 round-2 upgraded the scanner
// to walk all SelectorExpr; the formerly separate
// TestDomainAuthzMutation_BlindSpot_MethodValueAssignment self-check
// retired (form-complete scan absorbs its assertion).
//
// For DOMAIN-AUTHZ-FIELD-PRIVATE-01 — go/types struct/method inspection.
// AST forms NOT covered by the type definition check:
//
//  7. unsafe.Pointer offset write bypasses Go field visibility:
//     (*domain.UserStatus)(unsafe.Pointer(uintptr(unsafe.Pointer(u)) + offset))
//     Captured by:
//     TestDomainAuthzMutation_BlindSpot_UnsafePointerWrite (asserts absence).
//
//  8. reflect.ValueOf(u).Elem().FieldByName("status").Set(...):
//     Call-site reflection bypasses type checking. Captured by:
//     TestDomainAuthzMutation_BlindSpot_ReflectFieldByName (asserts absence).

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ─── Rule 1: DOMAIN-AUTHZ-FIELD-PRIVATE-01 ─────────────────────────────

// TestDomainAuthzFieldPrivate_01 enforces DOMAIN-AUTHZ-FIELD-PRIVATE-01:
// domain.User must NOT expose exported fields named Status,
// PasswordResetRequired, or AuthzEpoch, and must NOT have exported setter
// methods matching the Set*/Mark*/Clear*/Lock*/Unlock* pattern beyond the two
// sanctioned ones (SetStatus / SetPasswordResetRequired).
//
// Primary guarantee: Go field privatization makes cross-package writes of
// status/passwordResetRequired/authzEpoch a compile error. This test is the
// regression net that fires before a re-export ever reaches a build.
//
// Implementation: load the domain package via typeseval.SharedResolver, look
// up the User type via pkg.Types.Scope().Lookup("User"), then:
//  1. Inspect every struct field: exported field name in authzFieldNames → violation.
//  2. Inspect the pointer receiver method set: any exported method whose name
//     starts with a setter prefix and is NOT in sanctionedSetters → violation.
//
// RED fixture: testdata/authz_mutation_fixtures/domain_exported_authz_field_red
// contains a synthetic User struct with exported Status, PasswordResetRequired,
// AuthzEpoch fields and a SetStatusPublic method. The scanner must flag ≥ 1.
func TestDomainAuthzFieldPrivate_01(t *testing.T) {
	t.Parallel()
	Report(t, ruleDomainAuthzFieldPrivate01,
		CheckDomainAuthzFieldPrivate01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))

	// RED fixture verification.
	root := findModuleRoot(t)
	verifyDomainFieldRedFixtureDetected(
		t, root,
		"./tools/archtest/testdata/authz_mutation_fixtures/domain_exported_authz_field_red",
		"DOMAIN-AUTHZ-FIELD-PRIVATE-01 RED fixture",
	)
}

// ─── Rule 2: AUTHZ-MUTATION-APPLY-FUNNEL-01 ────────────────────────────

// TestAuthzMutationApplyFunnel_SetStatus_01 enforces Rule (a) of
// AUTHZ-MUTATION-APPLY-FUNNEL-01: every call to domain.User.SetStatus or
// domain.User.SetPasswordResetRequired in non-test production code must
// originate from an enclosing FuncDecl whose canonical identity
// (types.Func.FullName) is listed in setMutatorCallsiteAllowlist.
//
// callsite-level keying (issue #732): replaces the prior file-level allowlist.
// Any new function in an already-allowed file is NOT silently permitted; the
// reviewer must explicitly add a callsite entry.
//
// RED fixture: corecells/accesscore/internal/domain/testdata/rbacassign_direct_setstatus_red
// simulates an rbacassign caller invoking SetStatus directly — must detect ≥ 1.
func TestAuthzMutationApplyFunnel_SetStatus_01(t *testing.T) {
	t.Parallel()
	Report(t, ruleAuthzMutationApplyFunnel01,
		CheckAuthzMutationApplyFunnel01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))

	// RED fixture: rbacassign caller directly invoking SetStatus.
	// LOCATION: corecells/accesscore/internal/domain/testdata/ because domain is an
	// internal package; the fixture must live under corecells/accesscore/ to satisfy
	// Go's internal-import rule, and testdata/ keeps it out of go build ./...
	verifySetMutatorRedFixtureDetected(
		t,
		"./corecells/accesscore/internal/domain/testdata/rbacassign_direct_setstatus_red",
		domainSetStatusMethod,
		"AUTHZ-MUTATION-APPLY-FUNNEL-01 Rule (a) RED fixture",
	)
}

// ─── Rule 2 meta-invariant: stale-entry detection ──────────────────────

// TestAuthzMutationApplyFunnel_AllowlistEntriesAreLive enforces that every
// entry in setMutatorCallsiteAllowlist corresponds to ≥ 1 actual production
// callsite. Deleting the last caller of an allowlisted function makes the
// entry stale; this test fails to force same-PR cleanup.
//
// Mechanism: scan the same production tree as Rule (a), bucket each detected
// callsite by its caller identity (types.Func.FullName), and assert every
// allowlist key appears at least once.
//
// Test files are excluded — adding _test.go file caller of a setter does NOT
// keep an allowlist entry alive. The allowlist is for production callers only.
func TestAuthzMutationApplyFunnel_AllowlistEntriesAreLive(t *testing.T) {
	t.Parallel()

	hits := map[string]int{}
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/...",
		"./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				countAllowlistHits(p, file, domainSetStatusMethod, hits)
				countAllowlistHits(p, file, domainSetPasswordResetRequiredMethod, hits)
			}
			return nil
		})

	var stale []string
	for callerID := range setMutatorCallsiteAllowlist {
		if hits[callerID] == 0 {
			stale = append(stale, callerID)
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("AUTHZ-MUTATION-APPLY-FUNNEL-01 meta: allowlist entry %q has 0 production "+
			"callsites — last caller removed; delete the entry in the same PR", s)
	}
}

// ─── Form-completeness positive self-check ──────────────────────────────

// TestDomainAuthzMutation_ValueCapture_Detected positively asserts that the
// form-complete SelectorExpr scanner catches function-value capture of
// domain.User.SetStatus / SetPasswordResetRequired — the bypass form a
// CallExpr.Fun-only scanner would miss. Loads value_capture_setstatus_red,
// which captures the methods as values (var/return/arg-pass) without an
// immediate invocation; the scanner must emit ≥ 1 violation per form.
//
// This is the positive complement to TestAuthzMutationApplyFunnel_SetStatus_01
// (which asserts NO violations in production code): a form-uniqueness
// guarantee requires both "absent from production" AND "present in a fixture
// that exercises every covered form" so the scan is provably form-complete,
// not silently empty.
func TestDomainAuthzMutation_ValueCapture_Detected(t *testing.T) {
	t.Parallel()

	var found []Diagnostic
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/internal/domain/testdata/value_capture_setstatus_red",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				found = append(found,
					scanSetMutatorViolationsPass(p, file, rel, domainSetStatusMethod)...)
				found = append(found,
					scanSetMutatorViolationsPass(p, file, rel, domainSetPasswordResetRequiredMethod)...)
			}
			return nil
		})

	sort.Slice(found, func(i, j int) bool {
		if found[i].Rel != found[j].Rel {
			return found[i].Rel < found[j].Rel
		}
		return found[i].Line < found[j].Line
	})
	for _, d := range found {
		t.Logf("%s:%d: %s", d.Rel, d.Line, d.Message)
	}
	// Three banned references in the fixture: badValueCapture (SetStatus
	// assigned to var), badReturnDirect (SetPasswordResetRequired returned),
	// badArgPass (SetStatus passed as arg). Each must produce ≥ 1 violation.
	assert.GreaterOrEqual(t, len(found), 3,
		"form-completeness RED fixture must catch ≥ 3 value-capture references "+
			"(badValueCapture / badReturnDirect / badArgPass) — a CallExpr.Fun-only "+
			"scanner would miss all three. Got %d violation(s).", len(found))
}

// ─── Blind-spot self-check tests ─────────────────────────────────────────

// TestDomainAuthzMutation_BlindSpot_ReflectMethodByName asserts that
// reflect.Value.MethodByName("SetStatus") / ("SetPasswordResetRequired") does
// NOT appear in non-allowlisted production code, confirming the reflect blind
// spot is not exercised.
func TestDomainAuthzMutation_BlindSpot_ReflectMethodByName(t *testing.T) {
	t.Parallel()

	bannedNames := map[string]bool{
		domainSetStatusMethod:                true,
		domainSetPasswordResetRequiredMethod: true,
	}

	var violations []string
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/...", "./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, hit := range scanReflectStringArgCalls(p, file, reflectMethodByName,
					func(n string) bool { return bannedNames[n] }) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d: DOMAIN-AUTHZ-FIELD-PRIVATE-01: reflect.MethodByName(%q) blind spot "+
							"detected — archtest cannot see reflect-based invocations of authz setters",
						rel, hit.Line, hit.Name,
					))
				}
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"authz-mutation blind-spot: reflect.MethodByName of SetStatus / "+
			"SetPasswordResetRequired found in production code — the archtest cannot "+
			"see reflect-based invocations. Refactor to use authzmutate.Mutator.Apply.")
}

// TestDomainAuthzMutation_BlindSpot_UnsafePointerWrite asserts that
// unsafe.Pointer-based writes to domain.User authz fields do NOT appear in
// non-allowlisted production code. Such writes bypass Go's field visibility
// entirely and would be invisible to the type-definition check in
// DOMAIN-AUTHZ-FIELD-PRIVATE-01.
//
// Scanner: AST-only search for import of "unsafe" outside the allowlist.
// The unsafe package is legitimately used in adapters/postgres for pgx scanning;
// this check is scoped to corecells/accesscore/... and cmd/... and specifically
// flags packages that import "unsafe" outside the allowlist.
func TestDomainAuthzMutation_BlindSpot_UnsafePointerWrite(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/...", "./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, imp := range file.Imports {
					if imp.Path == nil {
						continue
					}
					impPath := strings.Trim(imp.Path.Value, `"`)
					if impPath == "unsafe" {
						line := p.Fset.Position(imp.Pos()).Line
						violations = append(violations, fmt.Sprintf(
							"%s:%d: imports \"unsafe\" — potential unsafe.Pointer write "+
								"could bypass domain.User authz field privatization "+
								"(blind spot for DOMAIN-AUTHZ-FIELD-PRIVATE-01)",
							rel, line,
						))
					}
				}
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"authz-mutation blind-spot: unsafe import found in corecells/accesscore or cmd/ — "+
			"verify no unsafe.Pointer writes target domain.User private fields.")
}

// TestDomainAuthzMutation_BlindSpot_ReflectFieldByName asserts that
// reflect.Value.FieldByName with authz field names does NOT appear in
// non-allowlisted production code. Such calls would bypass field privatization
// and be invisible to the type-definition check.
func TestDomainAuthzMutation_BlindSpot_ReflectFieldByName(t *testing.T) {
	t.Parallel()

	// Check both private field names (actual names) and potential exported regressions.
	bannedFieldNames := map[string]bool{
		"status":                true,
		"passwordResetRequired": true,
		"authzEpoch":            true,
		"Status":                true,
		"PasswordResetRequired": true,
		"AuthzEpoch":            true,
	}

	var violations []string
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/...", "./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, hit := range scanReflectStringArgCalls(p, file, reflectFieldByName,
					func(n string) bool { return bannedFieldNames[n] }) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d: DOMAIN-AUTHZ-FIELD-PRIVATE-01: reflect.FieldByName(%q) blind spot "+
							"detected — archtest cannot see reflect-based writes to domain.User authz fields",
						rel, hit.Line, hit.Name,
					))
				}
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"authz-mutation blind-spot: reflect.FieldByName of authz field names found "+
			"in production code — the archtest cannot see reflect-based field writes. "+
			"Refactor to use authzmutate.Mutator.Apply.")
}

// TestDomainAuthzMutation_BlindSpot_VarInitCall asserts (positively) that
// scanSetMutatorViolationsPass fires when a setter call is inside a
// package-level var-init expression (no enclosing FuncDecl).
//
// This is the §5 blind-spot entry in the package godoc. Implementation: load
// the RED fixture `var_init_setstatus_red` whose package-level var init
// invokes domain.User.SetStatus from outside any FuncDecl. The scanner must
// emit a violation containing "outside any FuncDecl" — proving
// ResolveEnclosingFunc → (nil, false) → automatic violation works end-to-end.
func TestDomainAuthzMutation_BlindSpot_VarInitCall(t *testing.T) {
	t.Parallel()

	var found []Diagnostic
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/internal/domain/testdata/var_init_setstatus_red",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				found = append(found,
					scanSetMutatorViolationsPass(p, file, rel, domainSetStatusMethod)...)
			}
			return nil
		})

	var hasOutsideFuncDecl bool
	for _, d := range found {
		t.Logf("%s:%d: %s", d.Rel, d.Line, d.Message)
		if strings.Contains(d.Message, "outside any FuncDecl") {
			hasOutsideFuncDecl = true
		}
	}
	assert.True(t, hasOutsideFuncDecl,
		"var-init blind-spot RED fixture must produce ≥ 1 violation containing "+
			"'outside any FuncDecl' — proves the scanner treats package-level "+
			"setter calls as automatic violations when ResolveEnclosingFunc "+
			"returns (nil, false). Got %d violation(s) without the expected substring.",
		len(found))
}

// ─── verifyDomainFieldRedFixtureDetected ─────────────────────────────────────

// verifyDomainFieldRedFixtureDetected loads the RED fixture package and asserts
// that the domain-field scanner finds ≥ 1 violation — proving the rule is not
// permanently GREEN.
func verifyDomainFieldRedFixtureDetected(t *testing.T, root, fixturePattern, label string) {
	t.Helper()
	_ = root // root is the module root; Run(t, WorkspaceTyped(...)) resolves it via findModuleRoot internally
	var found int
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{fixturePattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		found += len(scanDomainUserViolations(p))
		return nil
	})

	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"Check that the fixture actually exports authz fields or unauthorized setters.",
		label)
}

// ─── verifySetMutatorRedFixtureDetected ──────────────────────────────────────

// verifySetMutatorRedFixtureDetected loads the given RED fixture and asserts
// that the scanner finds ≥ 1 violation — proving the rule is not permanently GREEN.
func verifySetMutatorRedFixtureDetected(
	t *testing.T,
	fixturePattern, targetMethod, label string,
) {
	t.Helper()
	var found int
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{fixturePattern}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			found += len(scanSetMutatorViolationsPass(p, file, label, targetMethod))
		}
		return nil
	})

	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"Check that the fixture calls the banned method and is type-checkable.",
		label)
}
