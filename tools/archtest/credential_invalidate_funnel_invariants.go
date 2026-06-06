package archtest

// credential_invalidate_funnel_invariants.go — importable detection logic for
// five credential-invalidation funnel rules (#1302 M3).
//
// This is the non-test home of the Check* functions and all detection helpers
// for the five rules below, so an external Cell repository can compile and run
// them (Go never compiles a dependency's _test.go). GoCell's own Test* functions
// in credential_invalidate_funnel_invariants_test.go call the same Check* —
// single source, no parallel rule body.
//
// Platform-symbol paths are anchored to [PlatformModulePath] — no bare literals.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ─── rule ID constants ────────────────────────────────────────────────────────

const (
	ruleCredentialInvalidateFunnel01           = "CREDENTIAL-INVALIDATE-FUNNEL-01"
	ruleUserAuthzEpochBumpFunnel01             = "USER-AUTHZ-EPOCH-BUMP-FUNNEL-01"
	ruleRefreshRevokeUserFunnel01              = "REFRESH-REVOKE-USER-FUNNEL-01"
	ruleCredentialInvalidateUpstreamCaller01   = "CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01"
	ruleCredentialInvalidateApplierCanonical01 = "CREDENTIAL-INVALIDATE-APPLIER-INTERFACE-CANONICAL-01"
)

// ─── platform-symbol path constants (no bare literals) ───────────────────────

const (
	sessionStorePkg = PlatformModulePath + "/runtime/auth/session"
	userRepoPkg     = PlatformModulePath + "/cells/accesscore/internal/ports"
	refreshStorePkg = PlatformModulePath + "/runtime/auth/refresh"
	invalidatorPkg  = PlatformModulePath + "/cells/accesscore/internal/credentialinvalidate"
)

// Per-pkg sub-paths used in allowlist key derivation.
const (
	authzmutatePkg    = PlatformModulePath + "/cells/accesscore/internal/authzmutate"
	identitymanagePkg = PlatformModulePath + "/cells/accesscore/slices/identitymanage"
	rbacassignPkg     = PlatformModulePath + "/cells/accesscore/slices/rbacassign"
	sessionrefreshPkg = PlatformModulePath + "/cells/accesscore/slices/sessionrefresh"
)

const (
	sessionStoreType    = "Store"
	sessionRevokeMethod = "RevokeForSubject"

	userRepoType   = "UserRepository"
	userBumpMethod = "BumpAuthzEpoch"

	refreshStoreType    = "Store"
	refreshRevokeMethod = "RevokeUser"

	// CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 (S4d).
	invalidatorMethod = "Apply"
)

// upstreamCallerCallsiteAllowlist enumerates the exact production callsites
// (enclosing FuncDecl identities) permitted to invoke or capture
// credentialinvalidate.(*Invalidator).Apply. Keys are *types.Func.FullName()
// values (canonical Go reflection form); values document the rationale.
//
// Issue #732 upgrade: replaces the prior file-level upstreamCallerAllowlistPrefixes
// []string. Callsite-level keying eliminates the "same file / different function"
// slip path — any new function in an already-allowed slice MUST explicitly add a
// callsite entry.
//
// All five production callers are scanner-detectable. The pre-#1196 sessionrefresh
// blind spot (local invalidatorApplier interface in the caller package made
// info.Selections resolve Apply outside credentialinvalidate) is closed
// structurally by moving the interface to credentialinvalidate.Applier.
var upstreamCallerCallsiteAllowlist = map[string]string{
	"(*" + authzmutatePkg + ".Mutator).ApplyInTx": "" +
		"primary funnel — routes all live-aggregate authz mutations",
	"(*" + identitymanagePkg + ".Service).deleteUserAndRevokeTokens": "" +
		"co-tx atomicity: user-row delete + revoke in one transaction",
	"(*" + identitymanagePkg + ".Service).changePasswordInTx": "" +
		"co-tx atomicity: password write + revoke in one transaction",
	"(*" + rbacassignPkg + ".Service).persistChange": "" +
		"co-tx atomicity: role-row write + revoke in one transaction",
	"(*" + sessionrefreshPkg + ".Service).handleReuseDetected": "" +
		"reuse / stale-epoch cascade entry point (interface routing via credentialinvalidate.Applier)",
}

// funnelAllowlistPathPrefixes lists the module-relative path prefixes that
// are permitted to call each banned method directly (store implementations
// and the funnel itself).
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
	// ports.UserRepository conformance helper (FU-3 H1/K-B).
	"cells/accesscore/internal/ports/conformance/",
}

// isAllowlisted reports whether a module-relative path is in the funnel
// allowlist. Test files (*_test.go) are always allowed.
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

// ─── Rule 1: CheckCredentialInvalidateFunnel01 ───────────────────────────────

// CheckCredentialInvalidateFunnel01 runs CREDENTIAL-INVALIDATE-FUNNEL-01:
// session.Store.RevokeForSubject must only be called from the credentialinvalidate
// funnel or store implementations.
func CheckCredentialInvalidateFunnel01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
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
				ruleCredentialInvalidateFunnel01,
			)...)
		}
		return nil
	})

	sort.Strings(violations)
	var diags []Diagnostic
	for _, v := range violations {
		diags = append(diags, Diagnostic{Rel: v, Line: 0, Message: v})
	}
	return diags
}

// ─── Rule 2: CheckUserAuthzEpochBumpFunnel01 ─────────────────────────────────

// CheckUserAuthzEpochBumpFunnel01 runs USER-AUTHZ-EPOCH-BUMP-FUNNEL-01:
// ports.UserRepository.BumpAuthzEpoch must only be called from the
// credentialinvalidate funnel or repository implementations.
func CheckUserAuthzEpochBumpFunnel01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
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
				ruleUserAuthzEpochBumpFunnel01,
			)...)
		}
		return nil
	})

	sort.Strings(violations)
	var diags []Diagnostic
	for _, v := range violations {
		diags = append(diags, Diagnostic{Rel: v, Line: 0, Message: v})
	}
	return diags
}

// ─── Rule 3: CheckRefreshRevokeUserFunnel01 ──────────────────────────────────

// CheckRefreshRevokeUserFunnel01 runs REFRESH-REVOKE-USER-FUNNEL-01:
// refresh.Store.RevokeUser must only be called from the credentialinvalidate
// funnel or store implementations.
func CheckRefreshRevokeUserFunnel01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
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
				ruleRefreshRevokeUserFunnel01,
			)...)
		}
		return nil
	})

	sort.Strings(violations)
	var diags []Diagnostic
	for _, v := range violations {
		diags = append(diags, Diagnostic{Rel: v, Line: 0, Message: v})
	}
	return diags
}

// ─── Rule 4: CheckCredentialInvalidateUpstreamCaller01 ───────────────────────

// CheckCredentialInvalidateUpstreamCaller01 runs
// CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01: every reference to
// credentialinvalidate.(*Invalidator).Apply must originate from an enclosing
// FuncDecl listed in upstreamCallerCallsiteAllowlist.
func CheckCredentialInvalidateUpstreamCaller01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
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
				ruleCredentialInvalidateUpstreamCaller01,
			)...)
		}
		return nil
	})

	sort.Strings(violations)
	var diags []Diagnostic
	for _, v := range violations {
		diags = append(diags, Diagnostic{Rel: v, Line: 0, Message: v})
	}
	return diags
}

// ─── Rule 5: CheckCredentialInvalidateApplierCanonical01 ─────────────────────

// CheckCredentialInvalidateApplierCanonical01 runs
// CREDENTIAL-INVALIDATE-APPLIER-INTERFACE-CANONICAL-01: the Apply signature
// matching credentialinvalidate.Applier must only appear in the
// credentialinvalidate package.
func CheckCredentialInvalidateApplierCanonical01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	violations := scanApplierInterfaceCanonical(t, []string{
		"./cells/accesscore/internal/credentialinvalidate",
		"./cells/...",
		"./runtime/...",
		"./cmd/...",
	})
	sort.Strings(violations)
	var diags []Diagnostic
	for _, v := range violations {
		diags = append(diags, Diagnostic{Rel: v, Line: 0, Message: v})
	}
	return diags
}

// ─── scanUpstreamCallerViolationsPass ────────────────────────────────────────

// scanUpstreamCallerViolationsPass walks file's AST for every SelectorExpr
// resolving to (targetPkg, targetMethod) and emits a violation when the
// SelectorExpr's enclosing FuncDecl identity is NOT in
// upstreamCallerCallsiteAllowlist. Catches direct call AND function-value
// capture forms.
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

// ─── countUpstreamAllowlistHits ──────────────────────────────────────────────

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

// ─── scanFunnelViolationsPass ────────────────────────────────────────────────

// scanFunnelViolationsPass walks a single file's AST for EVERY SelectorExpr
// that resolves to the method (targetPkg, targetMethod) — regardless of whether
// it is the Fun of a CallExpr. It returns a violation string for each. Walking
// all selectors (not just call.Fun) makes the scan form-complete: it catches
// the direct call AND the function-value capture forms.
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

// ─── scanApplierInterfaceCanonical ───────────────────────────────────────────

// applierInterfaceCandidate records a *types.TypeName whose underlying type
// is an interface with an explicitly declared Apply method.
type applierInterfaceCandidate struct {
	pkgPath    string
	typeName   string
	methodType types.Type
	pos        token.Position
}

// scanApplierInterfaceCanonical loads the canonical credentialinvalidate
// package together with the scan-target patterns in a SINGLE Run(t, Typed(...))
// call, then compares each candidate interface's Apply signature against the
// canonical via types.Identical.
func scanApplierInterfaceCanonical(t *testing.T, patterns []string) []string {
	t.Helper()
	var canonical types.Type
	var candidates []applierInterfaceCandidate
	_ = Run(t, Typed(TypedOpts{Tests: false}, patterns), func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		if p.Pkg.Path() == invalidatorPkg {
			canonical = extractCanonicalApplierType(p)
			return nil
		}
		candidates = append(candidates, collectApplyCandidates(p)...)
		return nil
	})

	require.NotNil(t, canonical,
		"credentialinvalidate.Applier signature not captured — ensure "+
			"./cells/accesscore/internal/credentialinvalidate is in the patterns slice")

	return buildApplierViolations(candidates, canonical)
}

// extractCanonicalApplierType returns the Apply method type from
// credentialinvalidate.Applier, or nil if the type is absent or malformed.
func extractCanonicalApplierType(p *Pass) types.Type {
	applier := p.Pkg.Scope().Lookup("Applier")
	if applier == nil {
		return nil
	}
	iface, ok := applier.Type().Underlying().(*types.Interface)
	if !ok || iface.NumExplicitMethods() != 1 {
		return nil
	}
	return iface.ExplicitMethod(0).Type()
}

// collectApplyCandidates returns every named interface type in p that declares
// an explicit Apply method. It does NOT filter out the credentialinvalidate
// package itself — callers are responsible for skipping that package before
// calling this helper.
func collectApplyCandidates(p *Pass) []applierInterfaceCandidate {
	var out []applierInterfaceCandidate
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
		out = append(out, collectApplyMethodCandidates(p, tn, iface)...)
	}
	return out
}

// collectApplyMethodCandidates returns a candidate entry for each explicit
// Apply method found on iface.
func collectApplyMethodCandidates(p *Pass, tn *types.TypeName, iface *types.Interface) []applierInterfaceCandidate {
	var out []applierInterfaceCandidate
	for i := 0; i < iface.NumExplicitMethods(); i++ {
		m := iface.ExplicitMethod(i)
		if m.Name() != "Apply" {
			continue
		}
		pos := p.Fset.Position(tn.Pos())
		out = append(out, applierInterfaceCandidate{
			pkgPath:    p.Pkg.Path(),
			typeName:   tn.Name(),
			methodType: m.Type(),
			pos:        pos,
		})
	}
	return out
}

// buildApplierViolations returns a violation string for each candidate whose
// Apply signature is identical to canonical.
func buildApplierViolations(candidates []applierInterfaceCandidate, canonical types.Type) []string {
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
