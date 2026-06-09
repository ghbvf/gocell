// fence_token_mint_funnel.go — caller-allowlist funnel for
// credentialfence.Mint. Closes the upstream half of the credential-invalidate
// funnel: combined with the Go type system seal (FenceToken interface has an
// unexported isCredentialFenceToken marker method, so no external type can
// implement it) and the runtime nil-guard in the three mutation methods, this
// archtest yields upstream Hard via call-site form-uniqueness — the
// .claude/rules/gocell/ai-robust.md §Hard 范本目录 "typed marker funnel for
// unbounded ops" pattern.
//
// INVARIANT: FENCE-TOKEN-MINT-FUNNEL-01
//
// Rule: every non-test caller of credentialfence.Mint must live in one of the
// allowlisted production paths (the credentialinvalidate funnel + storetest /
// conformance suites). Any other caller is a violation: production code that
// is not the funnel must not be able to construct a FenceToken.
//
// AI-robust grade (Mint-caller dimension): Hard via call-site form-uniqueness
// per ai-robust.md §Hard 范本目录 "typed marker funnel for unbounded ops".
// The scanner is form-complete: it flags EVERY reference to credentialfence.Mint
// (direct call, var-decl / short-var function-value capture, return, pass-through
// arg, reflect arg) by walking all SelectorExpr and resolving each via
// ResolvePackageRef (alias-immune). Because credentialfence is type-sealed, a
// reference to Mint is the only way to obtain a FenceToken, so flagging every
// reference closes the function-value-capture and reflect blindspots
// structurally — there is no separate per-form blindspot self-check to keep in
// sync. Residual blindspots (dot-import, //go:linkname, unsafe) are the
// universal class that defeats any static analysis; see scanFenceTokenMintRefs.
//
// Grade caveat: enforcement is archtest-bound, not compile-time. Go cannot
// express "only package X may call function Y", so this is the Hard ceiling the
// rule's shape can reach (the PANIC-REGISTERED-01 precedent). The type-system
// Hard guarantee is the *construction* seal (external packages cannot implement
// FenceToken nor build the unexported impl); see runtime/auth/credentialfence.
//
// Companion archtests (the downstream half of the same funnel):
//   - CREDENTIAL-INVALIDATE-FUNNEL-01    — RevokeForSubject caller allowlist
//   - USER-AUTHZ-EPOCH-BUMP-FUNNEL-01    — BumpAuthzEpoch caller allowlist
//   - REFRESH-REVOKE-USER-FUNNEL-01      — RevokeUser caller allowlist
//   - CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 — Invalidator.Apply caller allowlist
//
// Together these four rules (downstream Hard) plus this rule (upstream Hard)
// fully close the credential-invalidation safety model. See ADR
// docs/architecture/202605101400-adr-credential-session-protocol.md §A16
// for the closure proof and threat-matrix re-evaluation.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green
// externally.
package archtest

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Mint funnel target: the credentialfence package path (derived from
// PlatformModulePath) and the Mint function name.
const (
	fenceTokenPkgPath  = PlatformModulePath + "/runtime/auth/credentialfence"
	fenceTokenMintFunc = "Mint"
)

// fenceTokenMintAllowlistPrefixes lists the module-relative path prefixes
// permitted to call credentialfence.Mint in non-test production code.
//
//   - corecells/accesscore/internal/credentialinvalidate/ — the production
//     funnel; Invalidator.Apply is the only call site that constructs a
//     FenceToken in real traffic.
//   - runtime/auth/session/storetest/, runtime/auth/refresh/storetest/,
//     corecells/accesscore/internal/ports/conformance/ — conformance suites that
//     must exercise the contract end-to-end; they need real FenceTokens to
//     call the mutation methods.
//
// _test.go files in any path are also allowed (see isFenceTokenMintAllowlisted).
//
// Mirrors the credential_invalidate_funnel_invariants_test.go allowlist
// philosophy: keep the set tight; any new caller is a code-review event that
// must add a path here. The reviewer's question — "should this package own a
// FenceToken?" — is the point.
var fenceTokenMintAllowlistPrefixes = []string{
	"corecells/accesscore/internal/credentialinvalidate/",
	"runtime/auth/session/storetest/",
	"runtime/auth/refresh/storetest/",
	"corecells/accesscore/internal/ports/conformance/",
}

// isFenceTokenMintAllowlisted reports whether rel is a permitted Mint caller.
// Test files (*_test.go) always pass — unit tests for stores, slice handlers,
// and integration suites legitimately need FenceTokens to drive call paths.
func isFenceTokenMintAllowlisted(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, prefix := range fenceTokenMintAllowlistPrefixes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// CheckFenceTokenMintFunnel enforces FENCE-TOKEN-MINT-FUNNEL-01.
//
// Every reference to credentialfence.Mint outside an allowlisted path is a
// violation — not just a direct call, but any function-value capture or
// reflect arg as well (see scanFenceTokenMintRefs for the form-complete scan).
// Combined with the upstream type-system seal (unexported marker method on
// FenceToken) the only way to obtain a non-nil FenceToken is through Mint;
// constraining every reference to Mint therefore constrains every non-nil
// FenceToken's origin.
//
// Scanner: ResolvePackageRef + EachInSubtree[ast.SelectorExpr]. credentialfence.Mint
// is a package-level function (not a method), so its reference is recorded in
// types.Info.Uses as a (PkgName, FuncName) pair — ResolveMethodCall would
// silently miss it (Selections only holds method selectors). The resolver
// returns the (pkgPath, name) tuple; a selector is a violation only when
// pkgPath == credentialfence package path and name == "Mint" — exact identity,
// no name-collision possible across packages, alias-immune.
//
// RED fixture verification: testdata/fence_token_fixtures/external_mint_red/
// is loaded separately and the scanner must detect all five reference forms
// (wantMin=5), proving the rule is form-complete and not a permanently-passing
// no-op (the reverse RED self-check mandated by
// ai-robust.md §"工具选定后强制盲区自检").
func CheckFenceTokenMintFunnel(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	// Scan production trees that could plausibly call Mint. examples/ and
	// cmd/ are included so a stray demo or composition root that fishes a
	// FenceToken on its own is caught.
	patterns := []string{
		"./corecells/...",
		"./runtime/...",
		"./adapters/...",
		"./cmd/...",
		"./examples/...",
	}

	var diags []Diagnostic
	_ = Run(t, WorkspaceTyped(TypedOpts{Tests: false}, patterns), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isFenceTokenMintAllowlisted(rel) {
				continue
			}
			diags = append(diags, scanFenceTokenMintRefs(p, file, rel)...)
		}
		return nil
	})

	// Report (via Canonical) dedups and orders diagnostics by
	// (Rel, Line, Message); no manual sort needed here.
	return diags
}

// scanFenceTokenMintRefs returns a structured Diagnostic for EVERY reference to
// credentialfence.Mint in file — regardless of the syntactic shape that
// references it. It walks all `*ast.SelectorExpr` nodes and resolves each via
// ResolvePackageRef, so it catches the direct call (`credentialfence.Mint()`),
// var-decl / short-var function-value capture (`var x = credentialfence.Mint`,
// `x := credentialfence.Mint`), return of the function value, pass-through as a
// call argument, and the reflect-arg form (`reflect.ValueOf(credentialfence.Mint)`).
//
// This is the form-complete replacement for the earlier CallExpr-only scanner
// plus its two enumerate-form blindspot self-checks: because credentialfence
// is sealed, the ONLY way for a non-funnel package to obtain a FenceToken is to
// reference Mint, so flagging every reference — not just the invocation —
// closes the function-value-capture and reflect blindspots structurally rather
// than asserting their absence one form at a time.
//
// Resolution: credentialfence.Mint is a package-level function (not a method),
// so its qualified reference lives in types.Info.Uses. ResolvePackageRef
// resolves the SelectorExpr (X is a PkgName, Sel is the function name) to the
// canonical (pkgPath, name) tuple — alias-immune (an `import cf "…"` rename
// resolves to the same path). The `sel.Sel.Name` pre-filter keeps the resolver
// off the hot path for unrelated selectors.
//
// Residual blindspots (universal class, not enumerated per-form): dot-import of
// credentialfence (`import . "…/credentialfence"` makes Mint a bare Ident — not
// used anywhere in this module, and a dot-import of an internal-style package
// would itself be an anomaly), `//go:linkname`, and `unsafe`. These defeat any
// static analysis and are out of scope for an archtest funnel.
func scanFenceTokenMintRefs(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != fenceTokenMintFunc {
			return
		}
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, sel)
		if !ok || pkgPath != fenceTokenPkgPath || name != fenceTokenMintFunc {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(sel.Pos()).Line,
			Message: "reference to credentialfence.Mint outside the funnel " +
				"(direct call or function-value capture)",
		})
	})
	return out
}

// verifyFenceTokenMintRedFixture loads the RED fixture and asserts the scanner
// detects at least wantMin violations. wantMin is the number of distinct
// credentialfence.Mint reference forms in the fixture (call / var-decl capture /
// short-var capture / pass-through arg / reflect arg). Requiring the full count
// — not merely ≥ 1 — proves the scanner is form-complete: a CallExpr-only
// scanner would catch only the single direct call and fall short, surfacing the
// regression here. Mirrors verifyRedFixtureDetectedPass in
// credential_invalidate_funnel_invariants_test.go.
func verifyFenceTokenMintRedFixture(t *testing.T, fixturePattern, label string, wantMin int) {
	t.Helper()

	var found []Diagnostic
	_ = Run(t, WorkspaceTyped(TypedOpts{Tests: false}, []string{fixturePattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			found = append(found, scanFenceTokenMintRefs(p, file, label)...)
		}
		return nil
	})

	require.GreaterOrEqual(t, len(found), wantMin,
		"RED fixture self-check FAILED: %s — expected ≥ %d violations, got %d. "+
			"A shortfall means the scanner is NOT form-complete (e.g. it only "+
			"catches direct CallExpr and misses function-value capture / reflect "+
			"arg forms), so a non-funnel package could construct a FenceToken "+
			"undetected. Check scanFenceTokenMintRefs covers every reference shape.",
		label, wantMin, len(found))

	// Structured-location contract: every diagnostic must carry a real Rel +
	// 1-based Line so Report renders "<rel>:<line>", not the ":0:" garbage an
	// empty-Rel Diagnostic{Message} produces (PR #1687 review C1 regression guard).
	for _, d := range found {
		require.NotEmpty(t, d.Rel,
			"%s: diagnostic must carry Rel (got empty): %+v", label, d)
		require.Greater(t, d.Line, 0,
			"%s: diagnostic must carry a 1-based Line (got %d): %+v", label, d.Line, d)
	}
}
