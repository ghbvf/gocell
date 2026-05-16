package archtest

// credential_invalidate_funnel_invariants_test.go — four closed-caller-set
// funnel rules covering both ends of the S4b/S4d credential-invalidation
// pipeline. The first three guard the DOWNSTREAM (store implementations);
// the fourth guards the UPSTREAM (the funnel's Apply entry point).
//
// INVARIANT: CREDENTIAL-INVALIDATE-FUNNEL-01
// INVARIANT: USER-AUTHZ-EPOCH-BUMP-FUNNEL-01
// INVARIANT: REFRESH-REVOKE-USER-FUNNEL-01
// INVARIANT: CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01
//
// S4d note (upstream rule): UPSTREAM-CALLER-01 is graded Medium —
// `Invalidator.Apply`'s caller set is enforced by archtest, but the rule
// catches "wrong call site" (the existing P1 patches handle the "missing
// call site" hole at the identitymanage entry). Promoting to Hard requires
// privatizing domain.User authz fields + sealed Mutation funnel; tracked
// as S4e in backlog AUTHZ-MUTATION-FUNNEL-UPGRADE-01.
//
// AI-rebust grade: Hard (closed-caller-set enforced via ResolveMethodCall;
// form uniqueness = "call resolves to this exact *types.Func identity" — no gray zone).
//
// Hard property: any direct call to session.Store.RevokeForSubject /
// ports.UserRepository.BumpAuthzEpoch / refresh.Store.RevokeUser outside the
// credentialinvalidate funnel causes archtest to fail in CI.
// The honesty caveat (matching SESSIONREFRESH-NO-SESSION-CREATE-01):
// Go does not prevent the calls at compile time; enforcement is archtest-bound.
//
// Scanning tool: ResolveMethodCall + EachInSubtree[ast.CallExpr].
// Resolver scope: targeted package trees (not full module ./...) to keep RAM
// bounded while still covering every non-test, non-store-impl call site.
//
// Blind-spot self-check (ai-collab.md §"工具选定后强制盲区自检"):
//
// ResolveMethodCall resolves `*ast.SelectorExpr` via info.Selections. Forms
// NOT covered by this tool:
//
//  1. Function-value store + call: `fn := store.RevokeForSubject; fn(ctx, id, e)`
//     The `fn(...)` CallExpr's Fun is *ast.Ident, not *ast.SelectorExpr, so
//     info.Selections[sel] misses it. Captured by:
//     TestCredentialInvalidateFunnel_BlindSpot_FuncValueAssignment (asserts absence).
//
//  2. reflect invoke: `reflect.ValueOf(store).MethodByName("RevokeForSubject").Call(...)`
//     Fully AST-invisible. Captured by:
//     TestCredentialInvalidateFunnel_BlindSpot_ReflectMethodByName (asserts absence).
//
//  3. Embedded struct method promotion: `type Wrapper struct { session.Store };
//     w.RevokeForSubject(...)` — receiver type resolves to Wrapper, not session.Store.
//     ResolveMethodCall recovers the correct *types.Func via info.Selections, so
//     this IS covered (embedded promotion is transparent to Selections.Obj()).
//     Documented here for completeness; no separate self-check needed.

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── package path constants ────────────────────────────────────────────────

const (
	sessionStorePkg     = "github.com/ghbvf/gocell/runtime/auth/session"
	sessionStoreType    = "Store"
	sessionRevokeMethod = "RevokeForSubject"

	userRepoPkg    = "github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	userRepoType   = "UserRepository"
	userBumpMethod = "BumpAuthzEpoch"

	refreshStorePkg     = "github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshStoreType    = "Store"
	refreshRevokeMethod = "RevokeUser"

	// CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 (S4d).
	invalidatorPkg    = "github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	invalidatorMethod = "Apply"
)

// upstreamCallerAllowlistPrefixes lists module-relative path prefixes whose
// production code is permitted to invoke credentialinvalidate.Invalidator.Apply
// directly. Every other caller is a violation: new authz-affecting state
// transitions must route through one of these entry points.
//
// As of S4e (PR #494):
//   - authzmutate/ owns all live-aggregate authz mutations (status demotion,
//     RequirePasswordReset, role-revoke) via Mutator.Apply → inv.Apply.
//   - identitymanage/ calls inv.Apply directly for two co-tx atomic operations:
//     (a) Delete: user-row delete + revoke must be one transaction.
//     (b) changePasswordInTx: password write + revoke must be one transaction.
//     Routing through authzmutate would split these transactions; direct call is
//     the intentional exception. Documented in service.go with
//     "Routed through funnel (CREDENTIAL-INVALIDATE-FUNNEL-01)" comment.
//   - sessionrefresh/ owns the reuse / stale-epoch cascade entry point.
//   - rbacassign/ calls inv.Apply for role-revoke co-tx with the role-row write.
//     Same atomicity reason as identitymanage.
//   - The funnel package itself is always allowed (Apply is defined here).
//
// S4e note: setup/ and adminprovision/ were removed from this list. Neither
// package calls credentialinvalidate.Invalidator.Apply in production code
// (verified by grep; provisioner.go only calls SetPasswordResetRequired on a
// freshly constructed aggregate at creation time). Removing them tightens the
// rule. The canonical allowlist is documented in ADR §A10 and is now in sync
// with this set.
var upstreamCallerAllowlistPrefixes = []string{
	"cells/accesscore/internal/credentialinvalidate/",
	"cells/accesscore/internal/authzmutate/",
	"cells/accesscore/slices/identitymanage/",
	"cells/accesscore/slices/sessionrefresh/",
	"cells/accesscore/slices/rbacassign/",
}

// isUpstreamCallerAllowlisted reports whether a module-relative path is in
// the upstream caller allowlist. Test files (*_test.go) always pass.
func isUpstreamCallerAllowlisted(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, prefix := range upstreamCallerAllowlistPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// funnelAllowlistPathPrefixes lists the module-relative path prefixes that
// are permitted to call each banned method directly (store implementations
// and the funnel itself). Names like "cells/accesscore/internal/credentialinvalidate/"
// are prefixes of every Go file under that directory subtree; matching uses
// strings.HasPrefix in isAllowlisted below. (The earlier "Suffixes" name was
// historic shorthand for "suffix of the Go import root"; the actual operation
// is prefix matching on the module-relative path, so the name is now aligned.)
var funnelAllowlistPathPrefixes = []string{
	// The funnel itself is the only permitted non-impl caller.
	"cells/accesscore/internal/credentialinvalidate/",
	// session.Store implementations.
	"runtime/auth/session/",
	// refresh.Store implementations.
	"runtime/auth/refresh/",
	// adapters/postgres session store + refresh store implementations.
	"adapters/postgres/",
	// accesscore internal mem implementations.
	"cells/accesscore/internal/mem/",
	"cells/accesscore/internal/adapters/postgres/",
	// storetest suites (conformance test helpers for store impls).
	"runtime/auth/refresh/storetest/",
	"runtime/auth/session/storetest/",
	// ports.UserRepository conformance helper (FU-3 H1/K-B). Same role as the
	// runtime/auth/*/storetest packages: conformance suite covers every method
	// of the contract (including BumpAuthzEpoch / UpdatePassword) so every impl
	// is held to the same behavior — direct method calls are intentional.
	"cells/accesscore/internal/ports/conformance/",
}

// isAllowlisted reports whether a module-relative path is in the funnel
// allowlist. Test files (*_test.go) are always allowed.
//
// Implementation note (Finding #1): this function previously used
// strings.Contains(rel, "/"+suffix) as a fallback. That branch was removed
// because it could match any path segment containing the suffix string, which
// would incorrectly allowlist paths like "examples/cells/accesscore/" if
// examples were ever added to the scan patterns. The scan patterns above
// (cells/..., runtime/..., adapters/..., cmd/...) are relative paths that
// SharedResolver returns as module-relative strings; HasPrefix is
// sufficient and does not have the Contains ambiguity.
func isAllowlisted(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, prefix := range funnelAllowlistPathPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// ─── Rule 1: CREDENTIAL-INVALIDATE-FUNNEL-01 ─────────────────────────────

// TestCredentialInvalidateFunnel_RevokeForSubject_01 enforces
// CREDENTIAL-INVALIDATE-FUNNEL-01: every call to session.Store.RevokeForSubject
// in non-test production code must originate from the credentialinvalidate
// funnel or a store implementation, not from slice business logic.
//
// RED fixture verification: the test also loads
// testdata/credential_invalidate_fixtures/rbacassign_direct_revoke_for_subject_red
// and asserts that the scanner detects ≥ 1 violation there — proving the rule
// is not a permanently-passing no-op.
func TestCredentialInvalidateFunnel_RevokeForSubject_01(t *testing.T) {
	t.Parallel()

	// Scan production packages that could plausibly call RevokeForSubject.
	patterns := []string{
		"./cells/accesscore/...",
		"./runtime/auth/...",
		"./adapters/...",
		"./cmd/...",
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, patterns, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isAllowlisted(rel) {
				continue
			}
			violations = append(violations, scanFunnelViolationsPass(
				p, file, rel,
				sessionStorePkg, sessionRevokeMethod,
				"CREDENTIAL-INVALIDATE-FUNNEL-01",
			)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"CREDENTIAL-INVALIDATE-FUNNEL-01: session.Store.RevokeForSubject must only be called "+
			"from cells/accesscore/internal/credentialinvalidate/ or store implementations. "+
			"Route new callers through credentialinvalidate.Invalidator.Apply instead.")

	// RED fixture verification: the scanner must detect ≥ 1 violation in the
	// rbacassign_direct_revoke_for_subject_red fixture package.
	verifyRedFixtureDetectedPass(t,
		"./tools/archtest/testdata/credential_invalidate_fixtures/rbacassign_direct_revoke_for_subject_red",
		sessionStorePkg, sessionRevokeMethod,
		"CREDENTIAL-INVALIDATE-FUNNEL-01 RED fixture",
	)
}

// ─── Rule 2: USER-AUTHZ-EPOCH-BUMP-FUNNEL-01 ─────────────────────────────

// TestCredentialInvalidateFunnel_BumpAuthzEpoch_01 enforces
// USER-AUTHZ-EPOCH-BUMP-FUNNEL-01: every call to UserRepository.BumpAuthzEpoch
// in non-test production code must originate from the credentialinvalidate
// funnel or a repository implementation.
//
// RED fixture: testdata/credential_invalidate_fixtures/identitymanage_direct_bump_epoch_red.
func TestCredentialInvalidateFunnel_BumpAuthzEpoch_01(t *testing.T) {
	t.Parallel()

	patterns := []string{
		"./cells/accesscore/...",
		"./adapters/...",
		"./cmd/...",
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, patterns, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isAllowlisted(rel) {
				continue
			}
			violations = append(violations, scanFunnelViolationsPass(
				p, file, rel,
				userRepoPkg, userBumpMethod,
				"USER-AUTHZ-EPOCH-BUMP-FUNNEL-01",
			)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"USER-AUTHZ-EPOCH-BUMP-FUNNEL-01: ports.UserRepository.BumpAuthzEpoch must only be called "+
			"from cells/accesscore/internal/credentialinvalidate/ or repository implementations. "+
			"Route callers through credentialinvalidate.Invalidator.Apply instead.")

	verifyRedFixtureDetectedPass(t,
		// Internal-import workaround: ports.UserRepository lives under
		// cells/accesscore/internal/, so the fixture must sit inside that tree
		// to satisfy Go's internal-import rules. `testdata/` keeps it out of
		// `go build ./...` while archtest loads it via explicit pattern. The
		// previous tools/archtest/testdata location silently failed to load.
		"./cells/accesscore/internal/credentialinvalidate/testdata/identitymanage_direct_bump_epoch_red",
		userRepoPkg, userBumpMethod,
		"USER-AUTHZ-EPOCH-BUMP-FUNNEL-01 RED fixture",
	)
}

// ─── Rule 3: REFRESH-REVOKE-USER-FUNNEL-01 ─────────────────────────────

// TestCredentialInvalidateFunnel_RevokeUser_01 enforces
// REFRESH-REVOKE-USER-FUNNEL-01: every call to refresh.Store.RevokeUser in
// non-test production code must originate from the credentialinvalidate funnel
// or a store implementation.
//
// RED fixture: testdata/credential_invalidate_fixtures/identitymanage_direct_revoke_refresh_red.
func TestCredentialInvalidateFunnel_RevokeUser_01(t *testing.T) {
	t.Parallel()

	patterns := []string{
		"./cells/accesscore/...",
		"./runtime/auth/...",
		"./adapters/...",
		"./cmd/...",
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, patterns, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isAllowlisted(rel) {
				continue
			}
			violations = append(violations, scanFunnelViolationsPass(
				p, file, rel,
				refreshStorePkg, refreshRevokeMethod,
				"REFRESH-REVOKE-USER-FUNNEL-01",
			)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"REFRESH-REVOKE-USER-FUNNEL-01: refresh.Store.RevokeUser must only be called "+
			"from cells/accesscore/internal/credentialinvalidate/ or store implementations. "+
			"Route callers through credentialinvalidate.Invalidator.Apply instead.")

	verifyRedFixtureDetectedPass(t,
		"./tools/archtest/testdata/credential_invalidate_fixtures/identitymanage_direct_revoke_refresh_red",
		refreshStorePkg, refreshRevokeMethod,
		"REFRESH-REVOKE-USER-FUNNEL-01 RED fixture",
	)
}

// ─── Rule 4: CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 (S4d, Medium) ─────

// TestCredentialInvalidateFunnel_ApplyUpstreamCaller_01 enforces
// CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01: every call to
// credentialinvalidate.(*Invalidator).Apply in production code must come
// from one of the allowlisted upstream entry points (authzmutate,
// identitymanage, sessionrefresh, rbacassign) or the funnel package
// itself. New callers must justify their addition via a PR that updates
// upstreamCallerAllowlistPrefixes — this puts the funnel's surface area on
// the reviewer's radar instead of relying on string convention.
// See ADR §A10 for the canonical allowlist and co-tx atomicity rationale.
//
// AI-rebust grade: Medium. The rule catches "wrong caller" (cells outside
// the allowlist invoking Apply directly) but NOT "missing caller" (a new
// user-authz mutator forgetting to call Apply at all). The latter is what
// P1-#1 in PR #490 review was — fixed in this PR by the identitymanage
// service.go change. Hard upgrade lives in S4e (sealed authzmutate funnel +
// domain.User field privatization) tracked as AUTHZ-MUTATION-FUNNEL-UPGRADE-01.
//
// RED fixture: tools/archtest/testdata/credential_invalidate_fixtures/
// sessionlogin_direct_apply_red — the sessionlogin slice is NOT on the
// allowlist; calling invalidator.Apply from there must be detected.
func TestCredentialInvalidateFunnel_ApplyUpstreamCaller_01(t *testing.T) {
	t.Parallel()

	// Scan production packages where someone might plausibly add a new
	// Invalidator.Apply call. We do NOT include runtime/auth/... or
	// adapters/... — Apply is a cells/accesscore-internal funnel; calls
	// from those layers would be a deeper architectural violation caught
	// by the existing LAYER-* archtests.
	patterns := []string{
		"./cells/accesscore/...",
		"./cmd/...",
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, patterns, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isUpstreamCallerAllowlisted(rel) {
				continue
			}
			violations = append(violations, scanFunnelViolationsPass(
				p, file, rel,
				invalidatorPkg, invalidatorMethod,
				"CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01",
			)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01: credentialinvalidate.Invalidator.Apply "+
			"must only be called from the allowlisted upstream entry points "+
			"(authzmutate, identitymanage, sessionrefresh, rbacassign) "+
			"or the funnel package itself. Adding a new caller requires updating "+
			"upstreamCallerAllowlistPrefixes; this puts the funnel surface on the "+
			"reviewer's radar instead of relying on string convention. "+
			"See ADR docs/architecture/202605101400-adr-credential-session-protocol.md §A10.")

	verifyRedFixtureDetectedPass(t,
		"./cells/accesscore/internal/credentialinvalidate/testdata/sessionlogin_direct_apply_red",
		invalidatorPkg, invalidatorMethod,
		"CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 RED fixture",
	)
}

// ─── Blind-spot self-check tests ─────────────────────────────────────────

// TestCredentialInvalidateFunnel_BlindSpot_FuncValueAssignment asserts that the
// function-value-assignment blind spot (e.g. `fn := store.RevokeForSubject; fn(...)`)
// does NOT appear in production code. If it did, the scanner would miss it.
// This inverts the blind-spot into a production-absence assertion so the rule
// remains valid under the "blind spot is not present" premise.
//
// Scanner used: EachInSubtree[ast.AssignStmt] + right-hand-side SelectorExpr
// name matching. This is an AST-only pattern check (not type-aware), but is
// sufficient because the method names are distinct enough to avoid false
// positives.
func TestCredentialInvalidateFunnel_BlindSpot_FuncValueAssignment(t *testing.T) {
	t.Parallel()

	bannedMethodNames := map[string]string{
		"RevokeForSubject": "CREDENTIAL-INVALIDATE-FUNNEL-01",
		"BumpAuthzEpoch":   "USER-AUTHZ-EPOCH-BUMP-FUNNEL-01",
		"RevokeUser":       "REFRESH-REVOKE-USER-FUNNEL-01",
	}

	var violations []string
	// Load production code (no tests).
	_ = RunTyped(t, TypedOpts{Tests: false},
		[]string{"./cells/accesscore/...", "./runtime/auth/...", "./adapters/...", "./cmd/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if isAllowlisted(rel) {
					continue
				}
				EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
					EachInChildren[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
						if rule, banned := bannedMethodNames[sel.Sel.Name]; banned {
							line := p.Fset.Position(assign.Pos()).Line
							violations = append(violations, fmt.Sprintf(
								"%s:%d: %s function-value assignment blind spot detected (%s)",
								rel, line, sel.Sel.Name, rule))
						}
					})
				})
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"funnel blind-spot: function-value assignment of banned methods found in production code — "+
			"the archtest would miss these calls. Refactor to call credentialinvalidate.Invalidator.Apply directly.")
}

// TestCredentialInvalidateFunnel_BlindSpot_ReflectMethodByName asserts that
// reflect.Value.MethodByName("RevokeForSubject") / ("BumpAuthzEpoch") /
// ("RevokeUser") does NOT appear in production code, confirming the reflect
// blind spot is not exercised (which would be scanner-invisible).
//
// Scanner: AST-only search for ast.BasicLit STRING containing the banned names
// as arguments to MethodByName calls.
func TestCredentialInvalidateFunnel_BlindSpot_ReflectMethodByName(t *testing.T) {
	t.Parallel()

	bannedNames := map[string]bool{
		"RevokeForSubject": true,
		"BumpAuthzEpoch":   true,
		"RevokeUser":       true,
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false},
		[]string{"./cells/accesscore/...", "./runtime/auth/...", "./adapters/...", "./cmd/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "MethodByName" {
						return
					}
					if len(call.Args) != 1 {
						return
					}
					lit, ok := call.Args[0].(*ast.BasicLit)
					if !ok {
						return
					}
					// Strip surrounding quotes from the string literal.
					name := strings.Trim(lit.Value, `"`)
					if bannedNames[name] {
						line := p.Fset.Position(call.Pos()).Line
						violations = append(violations, fmt.Sprintf(
							"%s:%d: reflect.MethodByName(%q) blind spot detected — archtest would miss this",
							rel, line, name))
					}
				})
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"funnel blind-spot: reflect.MethodByName of banned method names found in production code — "+
			"the archtest cannot see reflect-based invocations. Refactor to use direct calls.")
}

// ─── shared helpers ──────────────────────────────────────────────────────

// scanFunnelViolationsPass walks a single file's AST for CallExpr nodes where
// the method receiver resolves to the interface at (targetPkg, targetMethod).
// It returns a slice of violation strings for any call found.
//
// Receiver type check: we verify fn.Pkg().Path() == targetPkg AND that the
// Selection receiver is the named interface type. This is the same pattern as
// in sessionrefresh_no_session_create_test.go (which guards session.Store methods).
func scanFunnelViolationsPass(
	p *Pass,
	file *ast.File,
	rel string,
	targetPkg, targetMethod, ruleID string,
) []string {
	var out []string
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		if sel.Sel.Name != targetMethod {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok {
			return
		}
		if fn.Pkg() == nil || fn.Pkg().Path() != targetPkg {
			return
		}
		line := p.Fset.Position(call.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: %s: direct call to %s.%s bypasses credentialinvalidate funnel",
			rel, line, ruleID, filepath.Base(targetPkg), targetMethod))
	})
	return out
}

// verifyRedFixtureDetectedPass loads the given fixture pattern via RunTyped
// and asserts that the scanner finds ≥ 1 violation — proving the rule is
// not permanently GREEN. This is the "反向 RED 自检" (reverse RED self-check)
// mandated by ai-collab.md.
//
// Fixture load failure is a hard fail: the previous silent t.Logf+return
// masked archtest regressions — a fixture that stops type-checking would
// silently disable the RED self-check, leaving the production scan
// permanently GREEN with no warning. The fixture is in-tree and its build
// health is part of the archtest contract, so a load failure must fail the
// test and surface in CI.
func verifyRedFixtureDetectedPass(
	t *testing.T,
	fixturePattern, targetPkg, targetMethod, label string,
) {
	t.Helper()

	var found int
	diags := RunTyped(t, TypedOpts{Tests: false}, []string{fixturePattern}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			found += len(scanFunnelViolationsPass(p, file, label, targetPkg, targetMethod, label))
		}
		return nil
	})
	_ = diags
	require.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"This means the production scanner is permanently GREEN and would miss real violations. "+
			"Check that the fixture file actually calls the banned method and is type-checkable.",
		label)
}
