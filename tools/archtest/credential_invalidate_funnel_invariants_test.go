package archtest

// credential_invalidate_funnel_invariants_test.go — three closed-caller-set
// funnel rules for the S4b credential-invalidation trifecta.
//
// INVARIANT: CREDENTIAL-INVALIDATE-FUNNEL-01
// INVARIANT: USER-AUTHZ-EPOCH-BUMP-FUNNEL-01
// INVARIANT: REFRESH-REVOKE-USER-FUNNEL-01
//
// AI-rebust grade: Hard (closed-caller-set enforced via typeseval.ResolveMethodCall;
// form uniqueness = "call resolves to this exact *types.Func identity" — no gray zone).
//
// Hard property: any direct call to session.Store.RevokeForSubject /
// ports.UserRepository.BumpAuthzEpoch / refresh.Store.RevokeUser outside the
// credentialinvalidate funnel causes archtest to fail in CI.
// The honesty caveat (matching SESSIONREFRESH-NO-SESSION-CREATE-01):
// Go does not prevent the calls at compile time; enforcement is archtest-bound.
//
// Scanning tool: typeseval.ResolveMethodCall + EachInSubtree[ast.CallExpr].
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
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
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
)

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
// typeseval.SharedResolver returns as module-relative strings; HasPrefix is
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
	root := findModuleRoot(t)

	// Scan production packages that could plausibly call RevokeForSubject.
	patterns := []string{
		"./cells/accesscore/...",
		"./runtime/auth/...",
		"./adapters/...",
		"./cmd/...",
	}
	resolver, err := typeseval.SharedResolver(root, false, nil, patterns...)
	require.NoError(t, err, "typeseval.SharedResolver for production packages")

	var violations []string
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if isAllowlisted(rel) {
				continue
			}
			violations = append(violations, scanFunnelViolations(
				pkg, file, rel,
				sessionStorePkg, sessionRevokeMethod,
				"CREDENTIAL-INVALIDATE-FUNNEL-01",
			)...)
		}
	}

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
	verifyRedFixtureDetected(t, root,
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
	root := findModuleRoot(t)

	patterns := []string{
		"./cells/accesscore/...",
		"./adapters/...",
		"./cmd/...",
	}
	resolver, err := typeseval.SharedResolver(root, false, nil, patterns...)
	require.NoError(t, err, "typeseval.SharedResolver for production packages")

	var violations []string
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if isAllowlisted(rel) {
				continue
			}
			violations = append(violations, scanFunnelViolations(
				pkg, file, rel,
				userRepoPkg, userBumpMethod,
				"USER-AUTHZ-EPOCH-BUMP-FUNNEL-01",
			)...)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"USER-AUTHZ-EPOCH-BUMP-FUNNEL-01: ports.UserRepository.BumpAuthzEpoch must only be called "+
			"from cells/accesscore/internal/credentialinvalidate/ or repository implementations. "+
			"Route callers through credentialinvalidate.Invalidator.Apply instead.")

	verifyRedFixtureDetected(t, root,
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
	root := findModuleRoot(t)

	patterns := []string{
		"./cells/accesscore/...",
		"./runtime/auth/...",
		"./adapters/...",
		"./cmd/...",
	}
	resolver, err := typeseval.SharedResolver(root, false, nil, patterns...)
	require.NoError(t, err, "typeseval.SharedResolver for production packages")

	var violations []string
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if isAllowlisted(rel) {
				continue
			}
			violations = append(violations, scanFunnelViolations(
				pkg, file, rel,
				refreshStorePkg, refreshRevokeMethod,
				"REFRESH-REVOKE-USER-FUNNEL-01",
			)...)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"REFRESH-REVOKE-USER-FUNNEL-01: refresh.Store.RevokeUser must only be called "+
			"from cells/accesscore/internal/credentialinvalidate/ or store implementations. "+
			"Route callers through credentialinvalidate.Invalidator.Apply instead.")

	verifyRedFixtureDetected(t, root,
		"./tools/archtest/testdata/credential_invalidate_fixtures/identitymanage_direct_revoke_refresh_red",
		refreshStorePkg, refreshRevokeMethod,
		"REFRESH-REVOKE-USER-FUNNEL-01 RED fixture",
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
	root := findModuleRoot(t)

	// Load production code (no tests).
	resolver, err := typeseval.SharedResolver(root, false, nil,
		"./cells/accesscore/...", "./runtime/auth/...", "./adapters/...", "./cmd/...")
	require.NoError(t, err)

	bannedMethodNames := map[string]string{
		"RevokeForSubject": "CREDENTIAL-INVALIDATE-FUNNEL-01",
		"BumpAuthzEpoch":   "USER-AUTHZ-EPOCH-BUMP-FUNNEL-01",
		"RevokeUser":       "REFRESH-REVOKE-USER-FUNNEL-01",
	}

	var violations []string
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if isAllowlisted(rel) {
				continue
			}
			scanner.EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
				scanner.EachInChildren[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
					if rule, banned := bannedMethodNames[sel.Sel.Name]; banned {
						line := pkg.Fset.Position(assign.Pos()).Line
						violations = append(violations, fmt.Sprintf(
							"%s:%d: %s function-value assignment blind spot detected (%s)",
							rel, line, sel.Sel.Name, rule))
					}
				})
			})
		}
	}

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
	root := findModuleRoot(t)

	resolver, err := typeseval.SharedResolver(root, false, nil,
		"./cells/accesscore/...", "./runtime/auth/...", "./adapters/...", "./cmd/...")
	require.NoError(t, err)

	bannedNames := map[string]bool{
		"RevokeForSubject": true,
		"BumpAuthzEpoch":   true,
		"RevokeUser":       true,
	}

	var violations []string
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
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
					line := pkg.Fset.Position(call.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: reflect.MethodByName(%q) blind spot detected — archtest would miss this",
						rel, line, name))
				}
			})
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"funnel blind-spot: reflect.MethodByName of banned method names found in production code — "+
			"the archtest cannot see reflect-based invocations. Refactor to use direct calls.")
}

// ─── shared helpers ──────────────────────────────────────────────────────

// scanFunnelViolations walks a single file's AST for CallExpr nodes where the
// method receiver resolves to the interface at (targetPkg, targetMethod). It
// returns a slice of violation strings for any call found.
//
// Receiver type check: we verify fn.Pkg().Path() == targetPkg AND that the
// Selection receiver is the named interface type. This is the same pattern as
// in sessionrefresh_no_session_create_test.go (which guards session.Store methods).
func scanFunnelViolations(
	pkg *packages.Package,
	file *ast.File,
	rel string,
	targetPkg, targetMethod, ruleID string,
) []string {
	var out []string
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		if sel.Sel.Name != targetMethod {
			return
		}
		fn, ok := typeseval.ResolveMethodCall(pkg.TypesInfo, sel)
		if !ok {
			return
		}
		if fn.Pkg() == nil || fn.Pkg().Path() != targetPkg {
			return
		}
		line := pkg.Fset.Position(call.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: %s: direct call to %s.%s bypasses credentialinvalidate funnel",
			rel, line, ruleID, filepath.Base(targetPkg), targetMethod))
	})
	return out
}

// verifyRedFixtureDetected loads the given fixture pattern and asserts that
// the scanner finds ≥ 1 violation — proving the rule is not permanently GREEN.
// This is the "反向 RED 自检" (reverse RED self-check) mandated by ai-collab.md.
//
// Fixture load failure is now a hard fail (Finding #9 PR #490 review): the
// previous silent t.Logf+return masked archtest regressions — a fixture that
// stops type-checking would silently disable the RED self-check, leaving the
// production scan permanently GREEN with no warning. The fixture is in-tree
// (tools/archtest/testdata/...) and its build health is part of the archtest
// contract, so a load failure must fail the test and surface in CI.
func verifyRedFixtureDetected(
	t *testing.T,
	root, fixturePattern, targetPkg, targetMethod, label string,
) {
	t.Helper()
	resolver, err := typeseval.SharedResolver(root, false, nil, fixturePattern)
	require.NoError(t, err,
		"RED fixture load FAILED (%s): %v — a broken fixture silently disables the reverse self-check. "+
			"Repair the fixture or remove the rule; do not let this skip past.",
		label, err)
	var found int
	for _, pkg := range resolver.Packages() {
		if pkg == nil || pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			found += len(scanFunnelViolations(pkg, file, label, targetPkg, targetMethod, label))
		}
	}
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"This means the production scanner is permanently GREEN and would miss real violations. "+
			"Check that the fixture file actually calls the banned method and is type-checkable.",
		label)
}
