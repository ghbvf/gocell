package archtest

// fence_token_mint_funnel_test.go — caller-allowlist funnel for
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
// AI-robust grade: Hard (closed caller set enforced via ResolvePackageRef
// form-uniqueness; A1 resolves the SelectorExpr X to a *types.PkgName and
// requires (pkgPath, name) == (credentialfence, Mint); A2/A3 blindspot
// self-checks reject function-value capture and reflect invocations).
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

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mint funnel target. The pkg path constant is annotated with nolint:gosec
// because gosec G101 flags strings containing "credential" as potential
// hardcoded secrets; this is an import path used for type identity, not a
// credential value.
const (
	//nolint:gosec // G101 false positive: import path, not a credential
	fenceTokenPkgPath  = "github.com/ghbvf/gocell/runtime/auth/credentialfence"
	fenceTokenMintFunc = "Mint"
)

// fenceTokenMintAllowlistPrefixes lists the module-relative path prefixes
// permitted to call credentialfence.Mint in non-test production code.
//
//   - cells/accesscore/internal/credentialinvalidate/ — the production
//     funnel; Invalidator.Apply is the only call site that constructs a
//     FenceToken in real traffic.
//   - runtime/auth/session/storetest/, runtime/auth/refresh/storetest/,
//     cells/accesscore/internal/ports/conformance/ — conformance suites that
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
	"cells/accesscore/internal/credentialinvalidate/",
	"runtime/auth/session/storetest/",
	"runtime/auth/refresh/storetest/",
	"cells/accesscore/internal/ports/conformance/",
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

// TestFenceTokenMintFunnel_AllowlistEnforced enforces FENCE-TOKEN-MINT-FUNNEL-01.
//
// Every call to credentialfence.Mint outside an allowlisted path is a
// violation. Combined with the upstream type-system seal (unexported marker
// method on FenceToken) the only way to construct a non-nil FenceToken is
// through Mint; constraining Mint's callers therefore constrains every
// non-nil FenceToken's origin.
//
// Scanner: ResolvePackageRef + EachInSubtree[ast.CallExpr]. credentialfence.Mint
// is a package-level function (not a method), so its reference is recorded
// in types.Info.Uses as a (PkgName, FuncName) pair — ResolveMethodCall would
// silently miss it (Selections only holds method selectors). The resolver
// returns the (pkgPath, name) tuple; we accept the call only when
// pkgPath == credentialfence package path and name == "Mint" — exact
// identity, no name-collision possible across packages.
//
// RED fixture verification: testdata/fence_token_fixtures/external_mint_red/
// is loaded separately and the scanner must detect ≥ 1 violation, proving
// the rule is not a permanently-passing no-op (the reverse RED self-check
// mandated by ai-robust.md §"工具选定后强制盲区自检").
func TestFenceTokenMintFunnel_AllowlistEnforced(t *testing.T) {
	t.Parallel()

	// Scan production trees that could plausibly call Mint. examples/ and
	// cmd/ are included so a stray demo or composition root that fishes a
	// FenceToken on its own is caught.
	patterns := []string{
		"./cells/...",
		"./runtime/...",
		"./adapters/...",
		"./cmd/...",
		"./examples/...",
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, patterns, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isFenceTokenMintAllowlisted(rel) {
				continue
			}
			violations = append(violations, scanFenceTokenMintViolations(p, file, rel)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"FENCE-TOKEN-MINT-FUNNEL-01: credentialfence.Mint must only be called "+
			"from the credentialinvalidate funnel, storetest/conformance suites, "+
			"or *_test.go files. New callers require updating "+
			"fenceTokenMintAllowlistPrefixes — every addition is a review event "+
			"about whether this package should own a FenceToken at all.")

	// Reverse RED self-check: the scanner must catch the bypass attempt in
	// the external_mint_red fixture (calls Mint from a non-allowlisted path).
	verifyFenceTokenMintRedFixture(t,
		"./tools/archtest/testdata/fence_token_fixtures/external_mint_red",
		"FENCE-TOKEN-MINT-FUNNEL-01 RED fixture")
}

// TestFenceTokenMintFunnel_BlindSpot_FuncValueAssignment is the reverse
// blindspot self-check for function-value capture: `fn := credentialfence.Mint;
// fn()`. The right-hand side of the assignment is a *ast.SelectorExpr whose
// Sel is "Mint", but the subsequent CallExpr has Fun = *ast.Ident, which
// ResolveMethodCall cannot match against credentialfence.Mint. This test
// asserts the form does NOT appear in production code, keeping the
// FENCE-TOKEN-MINT-FUNNEL-01 rule complete under the "blindspot is absent"
// premise.
//
// Mirrors TestCredentialInvalidateFunnel_BlindSpot_FuncValueAssignment in
// credential_invalidate_funnel_invariants_test.go.
func TestFenceTokenMintFunnel_BlindSpot_FuncValueAssignment(t *testing.T) {
	t.Parallel()

	patterns := []string{
		"./cells/...", "./runtime/...", "./adapters/...", "./cmd/...", "./examples/...",
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, patterns, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if isFenceTokenMintAllowlisted(rel) {
				continue
			}
			EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
				EachInChildren[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
					if sel.Sel.Name != fenceTokenMintFunc {
						return
					}
					// Require sel.X == "credentialfence" identifier to reduce
					// false positives from same-name methods on unrelated types.
					xIdent, ok := sel.X.(*ast.Ident)
					if !ok || xIdent.Name != "credentialfence" {
						return
					}
					line := p.Fset.Position(assign.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: credentialfence.Mint function-value assignment blind spot detected "+
							"(FENCE-TOKEN-MINT-FUNNEL-01)",
						rel, line,
					))
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
		"FENCE-TOKEN-MINT-FUNNEL-01 blind-spot: function-value assignment of "+
			"credentialfence.Mint found in production code — the archtest would "+
			"miss subsequent invocations. Inline the call at the funnel site.")
}

// TestFenceTokenMintFunnel_BlindSpot_ReflectInvocation is the reverse
// blindspot self-check for reflect-based invocation. Production code must
// never use reflect.ValueOf to fetch / call Mint — such forms are
// AST-invisible to ResolveMethodCall. Asserts the form is absent.
//
// Mirrors TestCredentialInvalidateFunnel_BlindSpot_ReflectMethodByName.
func TestFenceTokenMintFunnel_BlindSpot_ReflectInvocation(t *testing.T) {
	t.Parallel()

	patterns := []string{
		"./cells/...", "./runtime/...", "./adapters/...", "./cmd/...", "./examples/...",
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{Tests: false}, patterns, func(p *Pass) []Diagnostic {
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
				if !ok {
					return
				}
				// reflect.ValueOf(credentialfence.Mint) — argument is the
				// SelectorExpr we care about.
				if sel.Sel.Name != "ValueOf" || len(call.Args) != 1 {
					return
				}
				xIdent, ok := sel.X.(*ast.Ident)
				if !ok || xIdent.Name != "reflect" {
					return
				}
				argSel, ok := call.Args[0].(*ast.SelectorExpr)
				if !ok {
					return
				}
				argIdent, ok := argSel.X.(*ast.Ident)
				if !ok || argIdent.Name != "credentialfence" {
					return
				}
				if argSel.Sel.Name != fenceTokenMintFunc {
					return
				}
				line := p.Fset.Position(call.Pos()).Line
				violations = append(violations, fmt.Sprintf(
					"%s:%d: reflect.ValueOf(credentialfence.Mint) blind spot detected "+
						"(FENCE-TOKEN-MINT-FUNNEL-01)",
					rel, line,
				))
			})
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"FENCE-TOKEN-MINT-FUNNEL-01 blind-spot: reflect.ValueOf(credentialfence.Mint) "+
			"found in production code — the archtest cannot see reflect-based invocations.")
}

// scanFenceTokenMintViolations returns violation strings for every CallExpr
// in file whose callee resolves to credentialfence.Mint.
//
// Resolution: credentialfence.Mint is a package-level function (not a
// method), so its qualified reference lives in types.Info.Uses, not in
// Selections. ResolvePackageRef walks the SelectorExpr (X is a PkgName,
// Sel is the function name) and returns the (pkgPath, name) tuple.
// ResolveMethodCall would silently miss this — it only resolves method
// selections.
func scanFenceTokenMintViolations(p *Pass, file *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok {
			return
		}
		if pkgPath != fenceTokenPkgPath || name != fenceTokenMintFunc {
			return
		}
		line := p.Fset.Position(call.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: FENCE-TOKEN-MINT-FUNNEL-01: direct call to credentialfence.Mint "+
				"bypasses the credentialinvalidate funnel",
			rel, line,
		))
	})
	return out
}

// verifyFenceTokenMintRedFixture loads the RED fixture and asserts the
// scanner detects at least one violation. Mirrors verifyRedFixtureDetectedPass
// in credential_invalidate_funnel_invariants_test.go.
func verifyFenceTokenMintRedFixture(t *testing.T, fixturePattern, label string) {
	t.Helper()

	var found int
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{fixturePattern}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			found += len(scanFenceTokenMintViolations(p, file, label))
		}
		return nil
	})
	require.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"The production scanner would be permanently GREEN and miss real "+
			"violations. Check that the fixture file actually calls "+
			"credentialfence.Mint and is type-checkable.",
		label)
}
