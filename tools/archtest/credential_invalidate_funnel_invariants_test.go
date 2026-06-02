package archtest

// credential_invalidate_funnel_invariants_test.go — five closed-caller-set
// funnel rules covering both ends of the S4b/S4d credential-invalidation
// pipeline. The first three guard the DOWNSTREAM (store implementations);
// the fourth guards the UPSTREAM (the funnel's Apply entry point); the
// fifth keeps the Applier interface canonical (declared only in the
// credentialinvalidate package) so callsite-level scanning stays uniform.
//
// INVARIANT: CREDENTIAL-INVALIDATE-FUNNEL-01
// INVARIANT: USER-AUTHZ-EPOCH-BUMP-FUNNEL-01
// INVARIANT: REFRESH-REVOKE-USER-FUNNEL-01
// INVARIANT: CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01
// INVARIANT: CREDENTIAL-INVALIDATE-APPLIER-INTERFACE-CANONICAL-01
//
// # AI-robust grade (post #1033 + #732)
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
// UPSTREAM-CALLER-01 (rule 4) is Hard post #1033 + #732. Five known
// production callers (authzmutate ApplyInTx, identitymanage Delete +
// changePasswordInTx, rbacassign persistChange, sessionrefresh
// handleReuseDetected) are all callsite-level allowlisted; the FenceToken
// seal (2)+(3) closes the "missing caller" hole structurally — a new
// mutator cannot revoke without a FenceToken it cannot mint.
//
// PR #1196 (#732) closes the prior sessionrefresh "interface-routed Soft
// channel": its invalidator field was typed as a local
// sessionrefresh.invalidatorApplier interface, which made info.Selections
// resolve Apply outside the credentialinvalidate package and hid the
// callsite from the scanner. The fix is structural — Applier is now an
// exported interface in credentialinvalidate, so info.Selections resolves
// Apply with Pkg=credentialinvalidate for all five callers uniformly, and
// the callsite-level allowlist reaches every production reference (direct
// call or function-value capture).
//
// See tools/archtest/fence_token_mint_funnel_test.go for the
// FENCE-TOKEN-MINT-FUNNEL-01 godoc, and ADR
// docs/architecture/202605101400-adr-credential-session-protocol.md §A16
// for the closure proof and threat-matrix re-evaluation.
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
// Interface-routed Apply via caller-package local interfaces is NOT a blind
// spot: archtest CREDENTIAL-INVALIDATE-APPLIER-INTERFACE-CANONICAL-01 forces
// the Applier interface to live in the credentialinvalidate package (not in
// any caller package), so info.Selections always resolves Apply with
// Pkg=credentialinvalidate. Pre-#1196 the sessionrefresh package defined a
// local invalidatorApplier interface that defeated this resolution; the fix
// is structural (Applier moved to credentialinvalidate). A regression
// reintroducing a caller-package local interface with method signature
// Apply(ctx, string, session.CredentialEvent) error would be caught by the
// canonical-interface archtest (see below).
//
// Covered without a separate self-check: embedded struct method promotion
// (`type Wrapper struct { session.Store }; w.RevokeForSubject(...)`) —
// ResolveMethodCall recovers the correct *types.Func via info.Selections, so
// promotion is transparent. The function-value-capture form (formerly a
// blind-spot self-check) is now caught directly by the form-complete scan.
//
// Rule 4 also has callsite-level identity invariants beyond the targetPkg
// filter:
//
//  4. Package-level var init bypass: a setter call at package scope has no
//     enclosing FuncDecl. ResolveEnclosingFunc returns (nil, false) and the
//     scan emits an "outside any FuncDecl" violation automatically — handled
//     in scanUpstreamCallerViolationsPass, no separate self-check needed
//     (the var-init blind spot for similar funnels is documented at
//     domain_authz_mutation_funnel_invariants_test.go §6).
//
//  5. FuncLit-inside-FuncDecl semantic choice (NOT a blind spot):
//     ResolveEnclosingFunc collapses a nested FuncLit's identity to its
//     outermost FuncDecl. Rationale: FuncLit author = FuncDecl author.
//     Affected callers: identitymanage.deleteUserAndRevokeTokens (Apply
//     inside RunInTx closure) — the closure's outer FuncDecl is correctly
//     resolved to deleteUserAndRevokeTokens. Same pattern for all other
//     allowlisted callers.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
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

// upstreamCallerCallsiteAllowlist enumerates the exact production callsites
// (enclosing FuncDecl identities) permitted to invoke or capture
// credentialinvalidate.(*Invalidator).Apply. Keys are *types.Func.FullName()
// values (canonical Go reflection form); values document the rationale.
//
// Issue #732 upgrade (this file, 2026-05-27): replaces the prior file-level
// upstreamCallerAllowlistPrefixes []string. callsite-level keying eliminates
// the "same file / different function" slip path — any new function in an
// already-allowed slice MUST explicitly add a callsite entry.
//
// As of S4e (PR #494), the legitimate callers are:
//
//   - authzmutate/ — Mutator.ApplyInTx routes all live-aggregate authz
//     mutations through Invalidator.Apply.
//   - identitymanage/ — Delete + changePasswordInTx call Invalidator.Apply
//     directly for co-tx atomicity (user-row delete and revoke, or password
//     write and revoke, must be one transaction). Routing through authzmutate
//     would split these transactions.
//   - sessionrefresh/ — handleReuseDetected owns the reuse / stale-epoch
//     cascade entry point.
//   - rbacassign/ — persistChange calls Invalidator.Apply co-tx with the
//     role-row write. Same atomicity reason as identitymanage.
//
// The funnel package itself (credentialinvalidate/) contains only the Apply
// implementation — no internal call site exists today, so no entry is
// needed. If a future helper inside the funnel calls Apply on a sibling
// receiver, an entry must be added explicitly (no silent package allowance).
//
// S4e note: setup/ and adminprovision/ are NOT in this list. Neither calls
// Invalidator.Apply in production code (provisioner.go only calls
// SetPasswordResetRequired on a freshly constructed aggregate at creation
// time). The canonical allowlist is documented in ADR §A10.
//
// Test files (*_test.go) bypass this check unconditionally. Removing the last
// production caller of an entry triggers
// TestCredentialInvalidateFunnel_AllowlistEntriesAreLive (meta-invariant),
// forcing same-PR cleanup.
// Allowlist keys are types.Func.FullName() values; split via string concat
// to keep lines under the lll limit while preserving the literal key.
//
// CI failure messages print the exact key to copy: look for
// `reference to credentialinvalidate.Apply from caller "<KEY>" not in
// upstreamCallerCallsiteAllowlist`. Paste the quoted "<KEY>" verbatim into
// this map.
//
// All five production callers are now scanner-detectable. The pre-#1196
// sessionrefresh blind spot (local invalidatorApplier interface in the
// caller package made info.Selections resolve Apply outside
// credentialinvalidate) is closed structurally by moving the interface to
// credentialinvalidate.Applier; sessionrefresh.Service.invalidator now has
// type credentialinvalidate.Applier, so info.Selections resolves Apply with
// fn.Pkg().Path() == credentialinvalidate and the scanner reaches the
// callsite check uniformly.
var upstreamCallerCallsiteAllowlist = map[string]string{
	"(*github.com/ghbvf/gocell/cells/accesscore/internal/authzmutate.Mutator).ApplyInTx": "" +
		"primary funnel — routes all live-aggregate authz mutations",
	"(*github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage.Service).deleteUserAndRevokeTokens": "" +
		"co-tx atomicity: user-row delete + revoke in one transaction",
	"(*github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage.Service).changePasswordInTx": "" +
		"co-tx atomicity: password write + revoke in one transaction",
	"(*github.com/ghbvf/gocell/cells/accesscore/slices/rbacassign.Service).persistChange": "" +
		"co-tx atomicity: role-row write + revoke in one transaction",
	"(*github.com/ghbvf/gocell/cells/accesscore/slices/sessionrefresh.Service).handleReuseDetected": "" +
		"reuse / stale-epoch cascade entry point (interface routing via credentialinvalidate.Applier)",
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
	_ = Run(t, Typed(TypedOpts{Tests: false}, patterns), func(p *Pass) []Diagnostic {
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
	_ = Run(t, Typed(TypedOpts{Tests: false}, patterns), func(p *Pass) []Diagnostic {
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
	_ = Run(t, Typed(TypedOpts{Tests: false}, patterns), func(p *Pass) []Diagnostic {
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

// ─── Rule 4: CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 (Hard, post #1033 + #732) ──

// TestCredentialInvalidateFunnel_ApplyUpstreamCaller_01 enforces
// CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01: every reference (call or
// function-value capture) to credentialinvalidate.(*Invalidator).Apply in
// production code must originate from an enclosing FuncDecl whose canonical
// identity is listed in upstreamCallerCallsiteAllowlist.
//
// callsite-level keying (issue #732): the prior file-level allowlist admitted
// any new function in an already-allowed slice silently. callsite identity =
// outermost FuncDecl's *types.Func.FullName() rejects that slip path: a new
// caller MUST add an explicit allowlist entry, putting the funnel surface
// directly on the reviewer's diff.
//
// AI-robust grade (post #1033 + #732), per ai-robust.md §"Funnel 双向锁评级":
// **Hard** uniformly across all five known production callers.
//
// Callsite identity is type-resolved via (ResolveMethodCall callee identity)
// + (ResolveEnclosingFunc caller identity); form-uniqueness applies. The
// "interface-routed channel" Soft residual from PR #1196 round-1/2 (where
// sessionrefresh held a local invalidatorApplier interface that made
// info.Selections resolve Apply outside credentialinvalidate) is closed
// structurally in round-3: Applier is an exported interface in the
// credentialinvalidate package, sessionrefresh.Service.invalidator has
// type credentialinvalidate.Applier, so all five callers' Apply references
// resolve with Pkg=credentialinvalidate uniformly.
//
// The "missing caller" problem (a new mutator forgetting to call Apply at
// all) is closed structurally by the sealed FenceToken capability proof
// (see package godoc + ADR §A16) — a new mutator that tries to revoke
// without going through Invalidator.Apply cannot mint a FenceToken.
//
// RED fixture: cells/accesscore/internal/credentialinvalidate/testdata/
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
	_ = Run(t, Typed(TypedOpts{Tests: false}, patterns), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			violations = append(violations, scanUpstreamCallerViolationsPass(
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
		"CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 (callsite-level): "+
			"credentialinvalidate.Invalidator.Apply must only be called from enclosing "+
			"functions explicitly listed in upstreamCallerCallsiteAllowlist. "+
			"Adding a new caller requires adding a callsite entry; "+
			"this puts the funnel surface on the reviewer's diff. "+
			"See ADR docs/architecture/202605101400-adr-credential-session-protocol.md §A10.")

	verifyRedFixtureDetectedPass(
		t,
		"./cells/accesscore/internal/credentialinvalidate/testdata/sessionlogin_direct_apply_red",
		invalidatorPkg, invalidatorMethod,
		"CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 RED fixture",
		1,
	)
}

// scanUpstreamCallerViolationsPass walks file's AST for every SelectorExpr
// resolving to (targetPkg, targetMethod) and emits a violation when the
// SelectorExpr's enclosing FuncDecl identity is NOT in
// upstreamCallerCallsiteAllowlist. Catches direct call AND function-value
// capture forms (same form-completeness as scanFunnelViolationsPass — see
// that function's godoc).
//
// A SelectorExpr located outside any FuncDecl (package-level var/const init)
// is an automatic violation: it has no allowlistable identity. Test files
// must be skipped by the caller (this function operates per-file but does
// not filter *_test.go itself).
func scanUpstreamCallerViolationsPass(
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
		caller, ok := ResolveEnclosingFunc(p.TypesInfo, file, sel)
		if !ok {
			out = append(out, fmt.Sprintf(
				"%s:%d: %s: reference to %s.%s outside any FuncDecl "+
					"(package-level init or similar) — cannot be allowlisted",
				rel, line, ruleID, filepath.Base(targetPkg), targetMethod,
			))
			return
		}
		callerID := caller.FullName()
		if _, allowed := upstreamCallerCallsiteAllowlist[callerID]; allowed {
			return
		}
		out = append(out, fmt.Sprintf(
			"%s:%d: %s: reference to %s.%s from caller %q not in "+
				"upstreamCallerCallsiteAllowlist (direct call or function-value capture) "+
				"(copy the quoted key verbatim into the map to allow)",
			rel, line, ruleID, filepath.Base(targetPkg), targetMethod, callerID,
		))
	})
	return out
}

// TestCredentialInvalidateFunnel_AllowlistEntriesAreLive enforces that every
// entry in upstreamCallerCallsiteAllowlist corresponds to ≥ 1 actual
// production reference (call or capture). Deleting the last caller of an
// allowlisted function makes the entry stale; this test fails to force
// same-PR cleanup.
//
// Test files are excluded — adding _test.go callers does NOT keep an entry
// alive. The allowlist tracks production callers only.
func TestCredentialInvalidateFunnel_AllowlistEntriesAreLive(t *testing.T) {
	t.Parallel()

	hits := map[string]int{}
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{
		"./cells/accesscore/...",
		"./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				countUpstreamAllowlistHits(p, file, hits)
			}
			return nil
		})

	var stale []string
	for callerID := range upstreamCallerCallsiteAllowlist {
		if hits[callerID] == 0 {
			stale = append(stale, callerID)
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 meta: allowlist entry %q "+
			"has 0 production references — last caller removed; delete the entry in "+
			"the same PR", s)
	}
}

// countUpstreamAllowlistHits increments hits[callerID] for each production
// SelectorExpr in file that resolves to credentialinvalidate.Invalidator.Apply
// AND has a resolvable enclosing FuncDecl matching the allowlist.
func countUpstreamAllowlistHits(p *Pass, file *ast.File, hits map[string]int) {
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != invalidatorMethod {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok || fn.Pkg() == nil || fn.Pkg().Path() != invalidatorPkg {
			return
		}
		caller, ok := ResolveEnclosingFunc(p.TypesInfo, file, sel)
		if !ok {
			return
		}
		callerID := caller.FullName()
		if _, allowed := upstreamCallerCallsiteAllowlist[callerID]; allowed {
			hits[callerID]++
		}
	})
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
	_ = Run(t, Typed(TypedOpts{Tests: false},
		[]string{"./cells/accesscore/...", "./runtime/auth/...", "./adapters/...", "./cmd/..."}),
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

// ─── Rule 5: CREDENTIAL-INVALIDATE-APPLIER-INTERFACE-CANONICAL-01 ──────

// TestCredentialInvalidateApplierInterfaceCanonical_01 enforces that the
// `Apply(ctx context.Context, subjectID string, event session.CredentialEvent)
// error` method signature appears in EXACTLY ONE production interface:
// credentialinvalidate.Applier. A caller package redeclaring an interface
// with the same signature would re-create the pre-#1196 sessionrefresh
// "interface-routed Soft channel" — info.Selections would resolve Apply to
// the caller-package interface, hiding the callsite from the
// CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 scan.
//
// Mechanism: load credentialinvalidate.Applier.Apply's *types.Func via the
// package scope; scan caller production packages (./cells/... ./runtime/...
// ./cmd/...) for any *types.TypeName whose underlying type is an interface
// that explicitly declares a method Apply whose *types.Signature is
// types.Identical to the canonical one. Skip the credentialinvalidate
// package itself; skip *_test.go (Tests:false).
//
// AI-robust grade: Medium (archtest-bound; type-resolved via go/types
// identity comparison — no string anchor, no name pattern). Hard upstream
// would require a sealed Applier (unexported marker method) but that
// breaks the spy-injection testability pattern; this archtest is the
// pragmatic backstop. Form-uniqueness: types.Identical on the method
// signature object — any other shape (same name + different signature, or
// same signature on a renamed method) does not match and is not the
// regression vector this rule guards.
//
// RED fixture: tools/archtest/testdata/credential_invalidate_fixtures/
// noncanonical_applier_interface_red declares
// `type LocalApplier interface { Apply(ctx, string, event) error }` in a
// non-credentialinvalidate package; the scanner must flag it.
func TestCredentialInvalidateApplierInterfaceCanonical_01(t *testing.T) {
	t.Parallel()

	// Production scan: load credentialinvalidate (canonical home) + caller
	// trees in ONE typed Run, so types.Identical sees matching stdlib
	// *types.Named instances. The canonical *types.Type is captured during
	// the same pass that records candidate interfaces; comparison runs
	// AFTER the typed Run returns (every package has been visited and canonical
	// is set).
	prodViolations := scanApplierInterfaceCanonical(t, []string{
		"./cells/accesscore/internal/credentialinvalidate",
		"./cells/...",
		"./runtime/...",
		"./cmd/...",
	})

	sort.Strings(prodViolations)
	for _, v := range prodViolations {
		t.Log(v)
	}
	assert.Empty(t, prodViolations,
		"CREDENTIAL-INVALIDATE-APPLIER-INTERFACE-CANONICAL-01: non-canonical "+
			"interface redeclares credentialinvalidate.Applier's Apply signature — "+
			"info.Selections would resolve Apply outside credentialinvalidate, "+
			"hiding the callsite from CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 scan. "+
			"Move the interface to the credentialinvalidate package "+
			"(or rename/reshape the method if the use case is unrelated).")

	// RED fixture: same single-Load pattern (canonical + fixture together).
	redViolations := scanApplierInterfaceCanonical(t, []string{
		"./cells/accesscore/internal/credentialinvalidate",
		"./tools/archtest/testdata/credential_invalidate_fixtures/noncanonical_applier_interface_red",
	})
	assert.GreaterOrEqual(t, len(redViolations), 1,
		"RED fixture self-check FAILED: noncanonical_applier_interface_red — "+
			"expected ≥ 1 violation, got %d. Check that the fixture declares an "+
			"interface with method Apply(ctx, string, session.CredentialEvent) error "+
			"and that ./cells/accesscore/internal/credentialinvalidate is in the same Load.",
		len(redViolations))
}

// applierInterfaceCandidate records a *types.TypeName whose underlying type
// is an interface with an explicitly declared Apply method, captured during
// a single typed Run pass. methodType is the *types.Signature of that Apply
// method, comparable via types.Identical against the canonical signature
// loaded by the same pass.
type applierInterfaceCandidate struct {
	pkgPath    string
	typeName   string
	methodType types.Type
	pos        token.Position
}

// scanApplierInterfaceCanonical loads the canonical credentialinvalidate
// package together with the scan-target patterns in a SINGLE Run(t, Typed(...)) call,
// then compares each candidate interface's Apply signature against the
// canonical via types.Identical.
//
// Single-Load constraint: types.Identical requires matching *types.Named
// instances for embedded stdlib types (e.g. context.Context). Separate
// packages.Load invocations produce distinct *types.Named for the same
// import path, defeating the comparison. Always pass the
// credentialinvalidate package together with scan targets in the same
// patterns slice.
func scanApplierInterfaceCanonical(t *testing.T, patterns []string) []string {
	t.Helper()
	var canonical types.Type
	var candidates []applierInterfaceCandidate
	_ = Run(t, Typed(TypedOpts{Tests: false}, patterns), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		if p.Pkg.Path() == invalidatorPkg {
			applier := p.Pkg.Scope().Lookup("Applier")
			if applier == nil {
				return nil
			}
			iface, ok := applier.Type().Underlying().(*types.Interface)
			if !ok || iface.NumExplicitMethods() != 1 {
				return nil
			}
			canonical = iface.ExplicitMethod(0).Type()
			return nil
		}
		scope := p.Pkg.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			iface, ok := tn.Type().Underlying().(*types.Interface)
			if !ok {
				continue
			}
			for i := 0; i < iface.NumExplicitMethods(); i++ {
				m := iface.ExplicitMethod(i)
				if m.Name() != "Apply" {
					continue
				}
				candidates = append(candidates, applierInterfaceCandidate{
					pkgPath:    p.Pkg.Path(),
					typeName:   tn.Name(),
					methodType: m.Type(),
					pos:        p.Fset.Position(tn.Pos()),
				})
			}
		}
		return nil
	})

	require.NotNil(t, canonical,
		"credentialinvalidate.Applier signature not captured — ensure "+
			"./cells/accesscore/internal/credentialinvalidate is in the patterns slice")

	var violations []string
	for _, c := range candidates {
		if types.Identical(c.methodType, canonical) {
			violations = append(violations, fmt.Sprintf(
				"%s: interface %s.%s declares Apply with the same signature "+
					"as credentialinvalidate.Applier — interface must live in "+
					"credentialinvalidate package",
				c.pos, c.pkgPath, c.typeName,
			))
		}
	}
	return violations
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

// verifyRedFixtureDetectedPass loads the given fixture pattern via Run(t, Typed(...)) and
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
	diags := Run(t, Typed(TypedOpts{Tests: false}, []string{fixturePattern}), func(p *Pass) []Diagnostic {
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
