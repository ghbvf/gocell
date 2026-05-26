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
// # AI-robust grade (post #1033)
//
// The credential-invalidation safety model rests on three mechanisms with
// distinct enforcement layers — do not conflate them into one "Hard":
//
//  1. DOWNSTREAM (rules 1–3) — Hard via call-site form-uniqueness, archtest-
//     bound (NOT compile-time). scanFunnelViolationsPass is form-complete: it
//     flags every reference to RevokeForSubject / BumpAuthzEpoch / RevokeUser
//     outside the allowlist — direct call AND function-value capture alike —
//     by resolving every SelectorExpr to the exact *types.Func identity. Go
//     cannot express "only package X may call method Y", so this is the Hard
//     ceiling the rule's shape can reach (same caveat as
//     SESSIONREFRESH-NO-SESSION-CREATE-01 / the PANIC-REGISTERED-01 precedent).
//
//  2. CONSTRUCTION SEAL — Hard, compile-time (Go type system). Types declared
//     outside runtime/auth/credentialfence cannot implement FenceToken
//     (unexported isCredentialFenceToken marker method); the concrete
//     fenceToken is itself unexported, so external composite literals cannot
//     construct it. This is the only genuinely compile-time guarantee here: a
//     non-funnel package cannot fabricate a FenceToken, so to invoke a mutation
//     method it must obtain one from Mint.
//
//  3. Mint-CALLER FUNNEL — Hard via call-site form-uniqueness, archtest-bound
//     (FENCE-TOKEN-MINT-FUNNEL-01). credentialfence.Mint references are locked
//     to the credentialinvalidate funnel, storetest / conformance suites, and
//     *_test.go files via a form-complete ResolvePackageRef scan. Combined with
//     (2), no non-test production package outside the funnel can produce a
//     FenceToken value.
//
// Runtime nil-guard (NOT a static grade): credentialfence.MustHave at the top
// of every mutation impl converts a literal `nil` argument — which compiles
// and is therefore invisible to (1)/(3) — into an immediate panic
// (panicregister.Approved + errcode.Assertion → 500). It is a defense-in-depth
// backstop for the residual "pass nil" form; it does NOT elevate any static
// grade, and the static Hard claims above stand on (1)+(2)+(3) alone.
//
// UPSTREAM-CALLER-01 (rule 4) upgraded Medium → Hard once (2)+(3) closed the
// "missing caller" hole structurally (a new mutator cannot revoke without a
// FenceToken it cannot mint). See tools/archtest/fence_token_mint_funnel_test.go
// for the FENCE-TOKEN-MINT-FUNNEL-01 godoc, and ADR
// docs/architecture/202605101400-adr-credential-session-protocol.md §A16 for
// the closure proof and threat-matrix re-evaluation.
//
// Scanning tool: ResolveMethodCall + EachInSubtree[ast.SelectorExpr]. The scan
// is form-complete — it resolves EVERY SelectorExpr (not only the Fun of a
// CallExpr), so the direct call AND the function-value capture forms
// (`fn := store.RevokeForSubject`, `var fn = store.RevokeForSubject`, return /
// pass-through of the method value) all resolve to the same *types.Func and are
// flagged. info.Selections records a MethodVal selection for a captured method
// value even when it is not immediately invoked. Resolver scope: targeted
// package trees (not full module ./...) to keep RAM bounded while still covering
// every non-test, non-store-impl reference site.
//
// Blind-spot self-check (ai-robust.md §"工具选定后强制盲区自检"):
//
// ResolveMethodCall resolves `*ast.SelectorExpr` via info.Selections. Forms
// NOT covered by this tool:
//
//  1. reflect invoke: `reflect.ValueOf(store).MethodByName("RevokeForSubject").Call(...)`
//     The method is named by string, so no SelectorExpr resolves to it — fully
//     AST-invisible. Asserted absent by:
//     TestCredentialInvalidateFunnel_BlindSpot_ReflectMethodByName.
//
//  2. //go:linkname / unsafe — the universal class that defeats any static
//     analysis; out of scope for an archtest funnel.
//
// Covered without a separate self-check: embedded struct method promotion
// (`type Wrapper struct { session.Store }; w.RevokeForSubject(...)`) —
// ResolveMethodCall recovers the correct *types.Func via info.Selections, so
// promotion is transparent. The function-value-capture form (formerly a
// blind-spot self-check) is now caught directly by the form-complete scan.

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
	// adapters/redis hosts a single session.Store decorator implementation
	// (CachingSessionStore — AUTH-CACHE-01). The decorator delegates
	// RevokeForSubject to its inner store verbatim; cache invalidation is
	// intentionally NOT performed there — the wrapper relies on the co-tx
	// user.AuthzEpoch bump (executed by credentialinvalidate.Apply) +
	// sessionvalidate's epoch invariant to neutralize stale cached views.
	// The allowlist is narrowed to the single file (not the whole package) so
	// any future *.go added under adapters/redis/ that names RevokeForSubject
	// directly is caught — only this decorator is permitted.
	"adapters/redis/session_cache_store.go",
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
	// wantMin=3: the fixture exercises three reference forms (direct call +
	// short-var capture + var-decl capture). Requiring all three pins the
	// form-completeness of the method-funnel scanner.
	verifyRedFixtureDetectedPass(
		t,
		"./tools/archtest/testdata/credential_invalidate_fixtures/rbacassign_direct_revoke_for_subject_red",
		sessionStorePkg, sessionRevokeMethod,
		"CREDENTIAL-INVALIDATE-FUNNEL-01 RED fixture",
		3,
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

	verifyRedFixtureDetectedPass(
		t,
		// Internal-import workaround: ports.UserRepository lives under
		// cells/accesscore/internal/, so the fixture must sit inside that tree
		// to satisfy Go's internal-import rules. `testdata/` keeps it out of
		// `go build ./...` while archtest loads it via explicit pattern. The
		// previous tools/archtest/testdata location silently failed to load.
		"./cells/accesscore/internal/credentialinvalidate/testdata/identitymanage_direct_bump_epoch_red",
		userRepoPkg, userBumpMethod,
		"USER-AUTHZ-EPOCH-BUMP-FUNNEL-01 RED fixture",
		1,
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

	verifyRedFixtureDetectedPass(
		t,
		"./tools/archtest/testdata/credential_invalidate_fixtures/identitymanage_direct_revoke_refresh_red",
		refreshStorePkg, refreshRevokeMethod,
		"REFRESH-REVOKE-USER-FUNNEL-01 RED fixture",
		1,
	)
}

// ─── Rule 4: CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 (Hard, post #1033) ──

// TestCredentialInvalidateFunnel_ApplyUpstreamCaller_01 enforces
// CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01: every call to
// credentialinvalidate.(*Invalidator).Apply in production code must come
// from one of the allowlisted upstream entry points (authzmutate,
// identitymanage, sessionrefresh, rbacassign) or the funnel package
// itself. New callers must justify their addition via a PR that updates
// upstreamCallerAllowlistPrefixes — this puts the funnel's surface area on
// the reviewer's radar instead of relying on string convention.
// See ADR §A10 + §A16 for the canonical allowlist and co-tx atomicity
// rationale, and the type-system seal that brings this rule to Hard.
//
// AI-robust grade (post #1033): Hard. The rule's call-site allowlist
// catches "wrong caller" (cells outside the allowlist invoking Apply
// directly). The "missing caller" problem — a new user-authz mutator
// forgetting to call Apply at all — is now closed structurally by the
// sealed FenceToken capability proof: any mutation method (BumpAuthzEpoch
// / RevokeForSubject / RevokeUser) requires a credentialfence.FenceToken
// that only credentialfence.Mint can produce, and Mint's callers are
// locked by FENCE-TOKEN-MINT-FUNNEL-01 to the funnel + storetest +
// conformance + *_test.go. A new mutator that tries to revoke without
// going through Invalidator.Apply cannot mint a FenceToken — production
// archtest fails. See the package godoc and ADR §A16 for the full
// closure proof.
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

	verifyRedFixtureDetectedPass(
		t,
		"./cells/accesscore/internal/credentialinvalidate/testdata/sessionlogin_direct_apply_red",
		invalidatorPkg, invalidatorMethod,
		"CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 RED fixture",
		1,
	)
}

// ─── Blind-spot self-check tests ─────────────────────────────────────────

// TestCredentialInvalidateFunnel_BlindSpot_ReflectMethodByName asserts that
// reflect.Value.MethodByName("RevokeForSubject") / ("BumpAuthzEpoch") /
// ("RevokeUser") does NOT appear in production code, confirming the reflect
// blind spot is not exercised (which would be scanner-invisible).
//
// Scanner: shared scanReflectStringArgCalls (REFLECT-STRING-ARG-SCANNER-01) —
// typed reflect.Value receiver gate + EvaluateConstString arg folding (covers
// raw-string / const / concat banned names passed to MethodByName).
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
						"%s:%d: CREDENTIAL-INVALIDATE-FUNNEL-01: reflect.MethodByName(%q) blind spot "+
							"detected — archtest cannot see reflect-based invocations",
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
		"funnel blind-spot: reflect.MethodByName of banned method names found in production code — "+
			"the archtest cannot see reflect-based invocations. Refactor to use direct calls.")
}

// ─── shared helpers ──────────────────────────────────────────────────────

// scanFunnelViolationsPass walks a single file's AST for EVERY SelectorExpr
// that resolves to the method (targetPkg, targetMethod) — regardless of whether
// it is the Fun of a CallExpr. It returns a violation string for each. Walking
// all selectors (not just call.Fun) makes the scan form-complete: it catches
// the direct call (`store.RevokeForSubject(...)`) AND the function-value
// capture forms (`fn := store.RevokeForSubject`, `var fn = store.RevokeForSubject`,
// `return store.RevokeForSubject`, pass-through as an argument) that a
// CallExpr-only scan misses (the later `fn(...)` has Fun = *ast.Ident, invisible
// to ResolveMethodCall). info.Selections records a MethodVal selection for a
// method value even when it is not immediately invoked, so ResolveMethodCall
// resolves the capture forms to the same *types.Func identity.
//
// The `sel.Sel.Name != targetMethod` pre-filter keeps ResolveMethodCall off the
// hot path for unrelated selectors. Receiver type check: fn.Pkg().Path() ==
// targetPkg (same identity pattern as sessionrefresh_no_session_create_test.go).
//
// Residual blindspot (asserted absent by TestCredentialInvalidateFunnel_BlindSpot_ReflectMethodByName):
// reflect.Value.MethodByName("RevokeForSubject") names the method by string, so
// no SelectorExpr resolves to it. //go:linkname / unsafe are the universal class.
func scanFunnelViolationsPass(
	p *Pass,
	file *ast.File,
	rel string,
	targetPkg, targetMethod, ruleID string,
) []string {
	var out []string
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != targetMethod {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok {
			return
		}
		if fn.Pkg() == nil || fn.Pkg().Path() != targetPkg {
			return
		}
		line := p.Fset.Position(sel.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: %s: reference to %s.%s outside credentialinvalidate funnel "+
				"(direct call or function-value capture)",
			rel, line, ruleID, filepath.Base(targetPkg), targetMethod,
		))
	})
	return out
}

// verifyRedFixtureDetectedPass loads the given fixture pattern via RunTyped and
// asserts the scanner finds ≥ wantMin violations — proving the rule is not
// permanently GREEN. This is the "反向 RED 自检" (reverse RED self-check)
// mandated by ai-robust.md. wantMin is the number of distinct banned-method
// reference forms in the fixture; for fixtures that also exercise function-value
// capture (rbacassign_direct_revoke_for_subject_red), wantMin > 1 pins
// form-completeness — a CallExpr-only scan would catch only the direct call and
// fall short here.
//
// Fixture load failure is a hard fail: the previous silent t.Logf+return masked
// archtest regressions — a fixture that stops type-checking would silently
// disable the RED self-check, leaving the production scan permanently GREEN with
// no warning. The fixture is in-tree and its build health is part of the
// archtest contract, so a load failure must fail the test and surface in CI.
func verifyRedFixtureDetectedPass(
	t *testing.T,
	fixturePattern, targetPkg, targetMethod, label string,
	wantMin int,
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
	require.GreaterOrEqual(t, found, wantMin,
		"RED fixture self-check FAILED: %s — expected ≥ %d violations, got %d. "+
			"A shortfall means the scanner is NOT form-complete (e.g. it only catches "+
			"direct calls and misses function-value capture), so a non-funnel package "+
			"could bypass the funnel undetected. Check scanFunnelViolationsPass walks "+
			"all SelectorExpr, and that the fixture is type-checkable.",
		label, wantMin, found)
}
