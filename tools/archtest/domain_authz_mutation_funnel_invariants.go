package archtest

// domain_authz_mutation_funnel_invariants.go — importable detection logic for
// two domain-authz-mutation funnel rules (#1302 M3).
//
// This is the non-test home of the Check* functions and all detection helpers
// for the two rules below, so an external Cell repository can compile and run
// them (Go never compiles a dependency's _test.go). GoCell's own Test* functions
// in domain_authz_mutation_funnel_invariants_test.go call the same Check* —
// single source, no parallel rule body.
//
// Platform-symbol paths are anchored to [PlatformModulePath] — no bare literals.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// ─── rule ID constants ────────────────────────────────────────────────────────

const (
	ruleDomainAuthzFieldPrivate01  = "DOMAIN-AUTHZ-FIELD-PRIVATE-01"
	ruleAuthzMutationApplyFunnel01 = "AUTHZ-MUTATION-APPLY-FUNNEL-01"
)

// ─── platform-symbol path constants (no bare literals) ───────────────────────

const (
	domainUserPkg = PlatformModulePath + "/cells/accesscore/internal/domain"
)

// Per-pkg sub-paths used in setMutatorCallsiteAllowlist key derivation.
// Note: adminprovisionPkg is new here; identitymanagePkg is declared in
// credential_invalidate_funnel_invariants.go (same package, single declaration).
const (
	adminprovisionPkg = PlatformModulePath + "/cells/accesscore/internal/adminprovision"
)

const (
	domainUserType = "User"

	domainSetStatusMethod                = "SetStatus"
	domainSetPasswordResetRequiredMethod = "SetPasswordResetRequired"
)

// authzFieldNames are the three authz-sensitive field names that must remain
// private in production domain.User.
var authzFieldNames = map[string]bool{
	"Status":                true,
	"PasswordResetRequired": true,
	"AuthzEpoch":            true,
}

// sanctionedSetters are the two exported mutator methods that ARE permitted on
// domain.User. Any other exported method whose name matches a setter-concept
// prefix and is not in this map is a violation.
var sanctionedSetters = map[string]bool{
	domainSetStatusMethod:                true,
	domainSetPasswordResetRequiredMethod: true,
}

// authzSetterPrefixes are method name prefixes that indicate a setter for
// authz-sensitive state. Methods with these prefixes that are not in
// sanctionedSetters are flagged.
var authzSetterPrefixes = []string{"Set", "Mark", "Clear", "Lock", "Unlock"}

// setMutatorCallsiteAllowlist enumerates the exact production callsites that
// may invoke domain.User.SetStatus or domain.User.SetPasswordResetRequired
// directly. Keys are *types.Func.FullName() values (canonical Go reflection
// form for the enclosing FuncDecl); values document the rationale per entry.
//
// Adding an entry requires explicit reviewer acknowledgement: a new entry
// means a function is bypassing authzmutate.Mutator.Apply, which is legitimate
// only at creation time (no live sessions exist). Any other case must route
// through Mutator.Apply.
//
// Removing the last code-level caller of an entry triggers
// TestAuthzMutationApplyFunnel_AllowlistEntriesAreLive (meta-invariant),
// forcing the entry to be deleted in the same PR — no stale allowance.
//
// CI failure messages print the exact key to copy: look for
// `direct call to domain.User.X from caller "<KEY>" not in setMutatorCallsiteAllowlist`.
// Paste the quoted "<KEY>" verbatim into this map.
//
// Verified zero production CallExprs to these setters in authzmutate/ and
// domain/ packages (PR #1196 issue #732 verification); package-level carve-outs
// removed in this PR. The two creation-time entries are the only legitimate
// callsites outside the authzmutate funnel.
//
// Test files (*_test.go) bypass this check unconditionally.
var setMutatorCallsiteAllowlist = map[string]string{
	"(*" + adminprovisionPkg + ".Provisioner).createAdminUser": "" +
		"creation-time: brand-new user (epoch=1), no live sessions exist; " +
		"authzmutate.Apply is for mutating existing principals",
	"(*" + identitymanagePkg + ".Service).Create": "" +
		"creation-time: brand-new user (epoch=1), no live sessions exist; " +
		"same rationale as adminprovision",
}

// ─── Rule 1: CheckDomainAuthzFieldPrivate01 ──────────────────────────────────

// CheckDomainAuthzFieldPrivate01 runs DOMAIN-AUTHZ-FIELD-PRIVATE-01:
// domain.User must NOT expose exported fields named Status,
// PasswordResetRequired, or AuthzEpoch, and must NOT have exported setter
// methods matching the Set*/Mark*/Clear*/Lock*/Unlock* pattern beyond the two
// sanctioned ones (SetStatus / SetPasswordResetRequired).
func CheckDomainAuthzFieldPrivate01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	patterns := []string{"./cells/accesscore/internal/domain"}

	runOnce := func(tags []string) []Diagnostic {
		var out []Diagnostic
		opts := TypedOpts{Tests: false}
		if len(tags) > 0 {
			opts.Tags = tags
		}
		_ = Run(t, Typed(opts, patterns), func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != domainUserPkg {
				return nil
			}
			out = append(out, scanDomainUserViolations(p.Pkg)...)
			return nil
		})
		return out
	}

	diags := runOnce(nil)
	if len(cfg.BuildTags) > 0 {
		// Deduplicate by Rel+Line+Message.
		seen := map[string]bool{}
		for _, d := range diags {
			seen[d.Rel+d.Message] = true
		}
		for _, d := range runOnce(cfg.BuildTags) {
			if !seen[d.Rel+d.Message] {
				diags = append(diags, d)
			}
		}
	}
	return diags
}

// ─── Rule 2: CheckAuthzMutationApplyFunnel01 ─────────────────────────────────

// CheckAuthzMutationApplyFunnel01 runs Rule (a) of AUTHZ-MUTATION-APPLY-FUNNEL-01:
// every call to domain.User.SetStatus or domain.User.SetPasswordResetRequired in
// non-test production code must originate from an enclosing FuncDecl whose
// canonical identity (types.Func.FullName) is listed in
// setMutatorCallsiteAllowlist.
func CheckAuthzMutationApplyFunnel01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	patterns := []string{
		"./cells/accesscore/...",
		"./cmd/...",
	}
	return runFunnelDualScan(t, cfg, patterns, func(p *Pass, file *ast.File, rel string) []Diagnostic {
		if strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		var out []Diagnostic
		out = append(out, scanSetMutatorViolationsPass(p, file, rel, domainSetStatusMethod)...)
		out = append(out, scanSetMutatorViolationsPass(p, file, rel, domainSetPasswordResetRequiredMethod)...)
		return out
	})
}

// ─── scanDomainUserViolations ────────────────────────────────────────────────

// scanDomainUserViolations inspects the User named type in pkg for exported
// authz fields and unauthorized exported setter methods.
func scanDomainUserViolations(pkg *types.Package) []Diagnostic {
	obj := pkg.Scope().Lookup(domainUserType)
	if obj == nil {
		return []Diagnostic{{
			Rel:  pkg.Path(),
			Line: 0,
			Message: fmt.Sprintf(
				"type %s not found in package %s",
				domainUserType, pkg.Path(),
			),
		}}
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return []Diagnostic{{
			Rel:  pkg.Path(),
			Line: 0,
			Message: fmt.Sprintf(
				"%s is not a named type in %s",
				domainUserType, pkg.Path(),
			),
		}}
	}

	var out []Diagnostic
	out = append(out, scanExportedAuthzFields(named, pkg.Path())...)
	out = append(out, scanUnauthorizedSetterMethods(named, pkg.Path())...)
	return out
}

// scanExportedAuthzFields returns a Diagnostic for each exported struct field
// whose name is in authzFieldNames.
func scanExportedAuthzFields(named *types.Named, pkgPath string) []Diagnostic {
	strct, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	var out []Diagnostic
	for i := 0; i < strct.NumFields(); i++ {
		f := strct.Field(i)
		if f.Exported() && authzFieldNames[f.Name()] {
			out = append(out, Diagnostic{
				Rel:  pkgPath,
				Line: 0,
				Message: fmt.Sprintf(
					"%s.%s has exported authz field %q — must be private",
					domainUserType, pkgPath, f.Name(),
				),
			})
		}
	}
	return out
}

// scanUnauthorizedSetterMethods returns a Diagnostic for each exported
// pointer-receiver method whose name matches an authzSetterPrefixes entry but
// is not in sanctionedSetters.
func scanUnauthorizedSetterMethods(named *types.Named, pkgPath string) []Diagnostic {
	mset := types.NewMethodSet(types.NewPointer(named))
	var out []Diagnostic
	for i := 0; i < mset.Len(); i++ {
		name := mset.At(i).Obj().Name()
		if !token.IsExported(name) || sanctionedSetters[name] {
			continue
		}
		if d, ok := unauthorizedSetterViolation(name, pkgPath); ok {
			out = append(out, d)
		}
	}
	return out
}

// unauthorizedSetterViolation returns a Diagnostic (and ok=true) when name
// matches an authzSetterPrefixes entry, otherwise returns (zero, false).
func unauthorizedSetterViolation(name, pkgPath string) (Diagnostic, bool) {
	for _, prefix := range authzSetterPrefixes {
		if strings.HasPrefix(name, prefix) {
			return Diagnostic{
				Rel:  pkgPath,
				Line: 0,
				Message: fmt.Sprintf(
					"%s.%s has unauthorized exported setter %q "+
						"(prefix %q); only SetStatus and SetPasswordResetRequired are sanctioned",
					domainUserType, pkgPath, name, prefix,
				),
			}, true
		}
	}
	return Diagnostic{}, false
}

// ─── scanSetMutatorViolationsPass ────────────────────────────────────────────

// scanSetMutatorViolationsPass walks a single file's AST for EVERY SelectorExpr
// resolving to (domainUserPkg, targetMethod) — direct call AND function-value
// capture alike. Each violation is keyed by the enclosing FuncDecl's canonical
// *types.Func.FullName(); references outside any FuncDecl (package-level var
// init) are automatic violations.
//
// Form-completeness rationale: ResolveMethodCall via info.Selections resolves a
// SelectorExpr to the same *types.Func regardless of whether it sits in
// CallExpr.Fun (direct call) or elsewhere (`fn := u.SetStatus`,
// `return u.SetStatus`, `someFunc(u.SetStatus)`). Walking only CallExpr.Fun
// would leave method-value capture as an AST-expressible bypass — failing the
// AI-robust §"Hard 范本目录" form-uniqueness requirement. Mirrors
// scanFunnelViolationsPass / scanUpstreamCallerViolationsPass in
// credential_invalidate_funnel_invariants.go.
func scanSetMutatorViolationsPass(
	p *Pass,
	file *ast.File,
	rel string,
	targetMethod string,
) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != targetMethod {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok {
			return
		}
		if fn.Pkg() == nil || fn.Pkg().Path() != domainUserPkg {
			return
		}
		line := p.Fset.Position(sel.Pos()).Line
		caller, ok := ResolveEnclosingFunc(p.TypesInfo, file, sel)
		if !ok {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"reference to domain.User.%s outside any FuncDecl "+
						"(package-level init or similar) — cannot be allowlisted",
					targetMethod,
				),
			})
			return
		}
		callerID := caller.FullName()
		if _, allowed := setMutatorCallsiteAllowlist[callerID]; allowed {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"reference to domain.User.%s from caller %q not in setMutatorCallsiteAllowlist "+
					"(direct call or function-value capture; copy the quoted key verbatim into the map to allow)",
				targetMethod, callerID,
			),
		})
	})
	return out
}

// ─── countAllowlistHits ──────────────────────────────────────────────────────

// countAllowlistHits increments hits[callerID] for each production SelectorExpr
// in file that resolves to a domain.User setter AND has a resolvable enclosing
// FuncDecl matching the allowlist. Mirrors scanSetMutatorViolationsPass'
// form-complete SelectorExpr walk (direct call + function-value capture both
// count). References outside any FuncDecl, or inside non-allowlisted callers,
// are ignored — this counter is only used by the meta-invariant to detect
// stale entries.
//
// Note: hits[callerID] accumulates across all references within a single
// FuncDecl — if `Service.Create` calls SetStatus twice, or captures it once
// and calls it once, hits["…Service.Create"] is 2. The meta-invariant only
// asserts ≥1, so an entry is considered stale only when ALL references in
// its FuncDecl are removed. This is intentional: the allowlist tracks "this
// function is a legitimate caller", not "exactly N references within this
// function".
func countAllowlistHits(p *Pass, file *ast.File, targetMethod string, hits map[string]int) {
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != targetMethod {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok || fn.Pkg() == nil || fn.Pkg().Path() != domainUserPkg {
			return
		}
		caller, ok := ResolveEnclosingFunc(p.TypesInfo, file, sel)
		if !ok {
			return
		}
		callerID := caller.FullName()
		if _, allowed := setMutatorCallsiteAllowlist[callerID]; allowed {
			hits[callerID]++
		}
	})
}
