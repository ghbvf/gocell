package archtest

// credential_authority_assert_funnel.go — importable detection logic for
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (#1302 M3 Batch D).
//
// Non-test home of the detector logic so it can be compiled by an external
// Cell repository (Go never compiles a dependency's _test.go).
// GoCell's own Test* functions in credential_authority_assert_funnel_test.go
// call the same Check* — single source, no parallel rule body.
//
// Platform-symbol paths are anchored to [PlatformModulePath].
// The scan SCOPE is the running module, supplied by the driver.

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

// ─── rule ID constant ──────────────────────────────────────────────────────

const ruleCredentialAuthorityAssertFunnel01 = "CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01"

// ─── platform-symbol path constants (no bare literals) ────────────────────

const (
	credAuthorityPkgPath = PlatformModulePath + "/corecells/accesscore/internal/credentialauthority"
	credSessionPkgPath   = PlatformModulePath + "/runtime/auth/session"
	credDomainUserPkg    = PlatformModulePath + "/corecells/accesscore/internal/domain"
)

// credDomainUserType is the unqualified name of the domain User type.
// Kept separate from the path so callers can use it as a symbol name.
const credDomainUserType = "User"

// ─── symbol name constants ─────────────────────────────────────────────────

const (
	credAuthorityFnName = "Assert"
	credSessionType     = "Session"
	credSessionViewType = "ValidateView"
	credCanAuthenticate = "CanAuthenticate"
	credPasswordVersion = "PasswordVersion"
	credRevokedAt       = "RevokedAt"
)

// ─── caller allowlist + scope helpers ─────────────────────────────────────

// assertCallerAllowlist limits callers of credentialauthority.Assert to
// these slice prefixes + the funnel package itself. _test.go files always
// pass (test helpers may call Assert to construct expectations).
//
// identitymanage is an allowed CALLER (downstream prong) but is deliberately
// NOT in sliceFunnelScopes (upstream prong): changePasswordInTx legitimately
// reads user.PasswordVersion for the CAS write, which the upstream prong would
// false-positive on. Its gate PLACEMENT is governed independently by
// CHANGEPASSWORD-INACTIVE-GATE-01. See ADR §A11.2 + §A16.
var assertCallerAllowlist = []string{
	"corecells/accesscore/internal/credentialauthority/", // funnel itself
	"corecells/accesscore/slices/sessionlogin/",
	"corecells/accesscore/slices/sessionrefresh/",
	"corecells/accesscore/slices/sessionvalidate/",
	"corecells/accesscore/slices/identitymanage/", // #1017 pre-mutation inactive gate
}

// sliceFunnelScopes are the slice prefixes whose production files MUST route
// CanAuthenticate / PasswordVersion / RevokedAt reads through Assert.
var sliceFunnelScopes = []string{
	"corecells/accesscore/slices/sessionlogin/",
	"corecells/accesscore/slices/sessionrefresh/",
	"corecells/accesscore/slices/sessionvalidate/",
}

// isAssertCallerAllowlisted reports whether a module-relative path may call
// credentialauthority.Assert directly. Test files always pass.
func isAssertCallerAllowlisted(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, prefix := range assertCallerAllowlist {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// isInSliceFunnelScope reports whether rel is under one of the three slice
// prefixes that MUST route through Assert.
func isInSliceFunnelScope(rel string) bool {
	for _, prefix := range sliceFunnelScopes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// ─── scan helpers (production detection) ──────────────────────────────────

// scanAssertCallSites flags every CallExpr in file whose callee resolves via
// ResolvePackageRef to credentialauthority.Assert.
func scanAssertCallSites(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !IsCallToPkgFunc(p.TypesInfo, call, credAuthorityPkgPath, credAuthorityFnName) {
			return
		}
		line := p.Fset.Position(call.Pos()).Line
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"call to %s.%s outside slice allowlist "+
					"(sessionlogin/, sessionrefresh/, sessionvalidate/)",
				credAuthorityPkgPath, credAuthorityFnName,
			),
		})
	})
	return out
}

// scanDirectCanAuthCalls flags direct CallExpr to (*domain.User).CanAuthenticate
// inside slice files that should route through Assert.
func scanDirectCanAuthCalls(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != credCanAuthenticate {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok {
			return
		}
		if fn.Pkg() == nil || fn.Pkg().Path() != credDomainUserPkg {
			return
		}
		line := p.Fset.Position(call.Pos()).Line
		out = append(out, Diagnostic{
			Rel:     rel,
			Line:    line,
			Message: "direct call to domain.(*User).CanAuthenticate outside credentialauthority.Assert",
		})
	})
	return out
}

// scanDirectFieldReads flags SelectorExpr reads of domain.User.PasswordVersion
// inside slice files. (RevokedAt is handled by SESSION-REVOKED-FIELD-ACCESS-01.)
func scanDirectFieldReads(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != credPasswordVersion {
			return
		}
		selection := p.TypesInfo.Selections[sel]
		if selection == nil {
			return
		}
		obj := selection.Obj()
		field, ok := obj.(*types.Var)
		if !ok || !field.IsField() {
			return
		}
		recv := selection.Recv()
		if recv == nil {
			return
		}
		owner := typeOwner(recv)
		if owner == nil {
			return
		}
		ownerPkg := owner.Pkg()
		if ownerPkg == nil ||
			ownerPkg.Path() != credDomainUserPkg ||
			owner.Name() != credDomainUserType {
			return
		}
		line := p.Fset.Position(sel.Pos()).Line
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"direct read of %s.%s.%s outside credentialauthority.Assert",
				ownerPkg.Path(), owner.Name(), sel.Sel.Name,
			),
		})
	})
	return out
}

// ─── callee-reference helpers (P2-B) ──────────────────────────────────────

// funnelCalleeHit is one value-capture reference of a funnel-protected callee.
type funnelCalleeHit struct {
	Line   int
	Callee string // funnelCalleeAssert or funnelCalleeCanAuth
}

// funnel callee labels — single source shared by resolveFunnelCallee (message
// + bucket key) and verifyFunnelCalleeReferenceRedFixtureDetected (per-callee
// assertion), so the two cannot drift.
const (
	funnelCalleeAssert  = "credentialauthority.Assert"
	funnelCalleeCanAuth = "domain.(*User).CanAuthenticate"
)

// collectFunnelCalleeReferenceHits is the single-source scan behind both the
// production assertion (scanFunnelCalleeReferences) and the per-callee RED
// fixture self-check. Pass 1 collects every CallExpr.Fun node identity; pass 2
// reports every SelectorExpr that typed-resolves to a funnel callee and is NOT
// at a CallExpr.Fun position (value capture in any expression slot).
func collectFunnelCalleeReferenceHits(p *Pass, file *ast.File) []funnelCalleeHit {
	directCallFuns := map[ast.Expr]struct{}{}
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		directCallFuns[call.Fun] = struct{}{}
	})

	var out []funnelCalleeHit
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if _, isDirect := directCallFuns[sel]; isDirect {
			return
		}
		callee, ok := resolveFunnelCallee(p.TypesInfo, sel)
		if !ok {
			return
		}
		out = append(out, funnelCalleeHit{Line: p.Fset.Position(sel.Pos()).Line, Callee: callee})
	})
	return out
}

// scanFunnelCalleeReferences flags every SelectorExpr that typed-resolves
// to a funnel-protected callee (credentialauthority.Assert or
// domain.(*User).CanAuthenticate) and is NOT at CallExpr.Fun position.
func scanFunnelCalleeReferences(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	for _, hit := range collectFunnelCalleeReferenceHits(p, file) {
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: hit.Line,
			Message: fmt.Sprintf(
				"%s referenced as value (not direct call) — bypasses "+
					"caller allowlist via deferred invocation",
				hit.Callee,
			),
		})
	}
	return out
}

// resolveFunnelCallee reports whether sel typed-resolves to a funnel-
// protected callee and returns a human-readable identifier.
func resolveFunnelCallee(info *types.Info, sel *ast.SelectorExpr) (string, bool) {
	if sel.Sel == nil {
		return "", false
	}
	// Method selector first — *types.Info.Selections covers method values.
	if selection := info.Selections[sel]; selection != nil {
		if fn, ok := selection.Obj().(*types.Func); ok {
			if isFunnelMethod(fn) {
				return funnelCalleeCanAuth, true
			}
		}
	}
	// Package-qualified function reference: *types.Info.Uses[sel.Sel]
	// returns the referenced *types.Func.
	if obj := info.Uses[sel.Sel]; obj != nil {
		if fn, ok := obj.(*types.Func); ok {
			if isFunnelPackageFunc(fn) {
				return funnelCalleeAssert, true
			}
		}
	}
	return "", false
}

func isFunnelPackageFunc(fn *types.Func) bool {
	if fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == credAuthorityPkgPath && fn.Name() == credAuthorityFnName
}

func isFunnelMethod(fn *types.Func) bool {
	if fn.Pkg() == nil || fn.Pkg().Path() != credDomainUserPkg || fn.Name() != credCanAuthenticate {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	recvType := sig.Recv().Type()
	if ptr, isPtr := recvType.(*types.Pointer); isPtr {
		recvType = ptr.Elem()
	}
	named, ok := recvType.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Name() == credDomainUserType
}

// ─── stripModuleRoot ───────────────────────────────────────────────────────

// stripModuleRoot turns an absolute filename produced by p.Fset.Position
// into a module-relative path for assert messages. Best-effort: if the
// module-root marker is missing, returns the absolute path unchanged.
func stripModuleRoot(abs string) string {
	const marker = "/gocell/"
	if i := strings.LastIndex(abs, marker); i >= 0 {
		return abs[i+len(marker):]
	}
	// Worktree-aware fallback: file paths under worktrees/<NN>-<name>/
	// strip the marker manually.
	if i := strings.Index(abs, "/worktrees/"); i >= 0 {
		rest := abs[i+len("/worktrees/"):]
		if j := strings.Index(rest, "/"); j >= 0 {
			return rest[j+1:]
		}
	}
	return abs
}

// ─── typeOwner (moved from helpers_test.go) ───────────────────────────────

// typeOwner unwraps a *T → T and returns the *types.TypeName for the owning
// named type, or nil if the type isn't a *types.Named. Shared by the typed
// field/method receiver checks across funnel and reflect blind-spot archtests
// (credential_authority / session_revoked / reflect_string_arg).
//
// Moved from helpers_test.go to this non-test file so that non-test .go files
// (credential_authority_assert_funnel.go, etc.) can reference it directly
// without depending on a _test.go helper.
func typeOwner(t types.Type) *types.TypeName {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	return named.Obj()
}

// ─── CheckCredentialAuthorityAssertFunnel01 sub-runners ──────────────────

// collectDownstreamCallerViolations runs the downstream caller-allowlist
// prong and returns any violations.
func collectDownstreamCallerViolations(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return runFunnelDualScan(t, cfg, []string{
		"./corecells/...",
		"./cmd/...",
	}, func(p *Pass, file *ast.File, rel string) []Diagnostic {
		if isAssertCallerAllowlisted(rel) {
			return nil
		}
		return scanAssertCallSites(p, file, rel)
	})
}

// collectUpstreamMandatoryViolations runs the upstream mandatory-funnel prong
// and returns any violations.
func collectUpstreamMandatoryViolations(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return runFunnelDualScan(t, cfg, []string{
		"./corecells/accesscore/slices/sessionlogin/...",
		"./corecells/accesscore/slices/sessionrefresh/...",
		"./corecells/accesscore/slices/sessionvalidate/...",
	}, func(p *Pass, file *ast.File, rel string) []Diagnostic {
		if strings.HasSuffix(rel, "_test.go") || !isInSliceFunnelScope(rel) {
			return nil
		}
		return scanUpstreamMandatoryFile(p, file, rel)
	})
}

// scanUpstreamMandatoryFile collects the direct-canAuth-call and
// direct-field-read violations for a single sliced production file.
func scanUpstreamMandatoryFile(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	out = append(out, scanDirectCanAuthCalls(p, file, rel)...)
	out = append(out, scanDirectFieldReads(p, file, rel)...)
	return out
}

// collectCalleeReferenceViolations runs the callee-reference (no value-capture)
// prong and returns any violations.
func collectCalleeReferenceViolations(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	return runFunnelDualScan(t, cfg, []string{
		"./corecells/...",
		"./cmd/...",
		"./runtime/...",
	}, func(p *Pass, file *ast.File, rel string) []Diagnostic {
		if strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		return scanFunnelCalleeReferences(p, file, rel)
	})
}

// ─── CheckCredentialAuthorityAssertFunnel01 ───────────────────────────────

// CheckCredentialAuthorityAssertFunnel01 runs CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01
// over the running module and returns its diagnostics.
//
// This is the importable CellRule body. GoCell's own Test* functions in
// credential_authority_assert_funnel_test.go call the same detectors —
// single source, no parallel rule body.
//
// The rule is intentionally NOT registered in StandardCellRules: it reasons
// about GoCell's own internal package layout (credentialauthority / domain /
// session slices), making it vacuous-green or false-red for an external module.
func CheckCredentialAuthorityAssertFunnel01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	diags = append(diags, collectDownstreamCallerViolations(t, cfg)...)
	diags = append(diags, collectUpstreamMandatoryViolations(t, cfg)...)
	diags = append(diags, collectCalleeReferenceViolations(t, cfg)...)
	return diags
}
