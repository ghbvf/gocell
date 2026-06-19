//go:build archtest

// authz_authorize_as_caller_funnel_test.go — caller-allowlist funnel for the
// explicit-subject authorization seam auth.SubjectAuthorizer.AuthorizeAs.
//
// INVARIANT: AUTHZ-AUTHORIZE-AS-CALLER-FUNNEL-01
//
// # What this guards
//
// auth.SubjectAuthorizer.AuthorizeAs authorizes an EXPLICITLY-supplied subject
// (a sealed auth.SubjectDescriptor) decoupled from the ambient ctx Principal
// (#1904 batch A). It is the reuse seam non-HTTP paths need (device cert signing,
// background reconcile) — but in the wrong hands it is a subject-forgery bypass:
// a business HTTP handler could mint a device SubjectDescriptor and ask the PDP
// "is THIS subject allowed", sidestepping the authenticated-principal gate that
// the normal Authorize path enforces. Business code MUST go through the
// principal-based Authorize / RequirePermission path; only the sanctioned cert
// adapter (and the framework lazy delegation that feeds it) may call AuthorizeAs.
//
// # Caller allowlist (downstream)
//
//   - framework/runtime/certsigning/pdpauthz/pdpauthz.go — the PDP-backed
//     certsigning.Authorizer adapter, the SOLE business consumer of the seam.
//   - framework/runtime/bootstrap/primary_authorizer_option.go — the
//     lazyAuthorizer delegation: it implements auth.SubjectAuthorizer by
//     forwarding to the resolved PDP, so the composition root can hand one
//     lazily-resolved engine to the cert adapter. This is framework plumbing
//     (deferred resolution), not subject forgery; the value it forwards is built
//     by the adapter, never minted here.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream (who may CALL AuthorizeAs): Medium. The detector resolves every
//     identifier USE of the AuthorizeAs method to the auth.SubjectAuthorizer
//     interface *types.Func via go/types (info.Uses), so package-qualified,
//     import-aliased, and method-value-capture forms all resolve to the same
//     object; any reference outside the allowlist fails CI. Honest caveat: Go
//     does not block the call at compile time (AuthorizeAs is an exported
//     interface method), so enforcement is archtest-bound — the highest grade
//     reachable for an exported-method caller restriction (Go ceiling, same as
//     AUTHZ-DECISION-ALLOW-DENY-CALLER-01).
//   - Upstream (can the explicit-subject path forge a privileged subject):
//     Hard. auth.SubjectDescriptor has unexported fields and only device/system
//     constructors (no admin/super-admin constructor exists), so a privileged
//     descriptor cannot be minted at all (sealed construction, batch A).
//
// Detection is use-based (info.Uses), invariant to import / call-vs-value form,
// so it also covers method-value capture (f := pdp.AuthorizeAs) — a captured
// value referenced outside the allowlist is flagged exactly like a direct call.
//
// Anti-vacuity: each allowlist entry MUST reference AuthorizeAs at least once, so
// a removed call or a scanner regression (which would make the funnel vacuously
// pass) fails CI. A RED fixture (internal/authorizeascallerfixture) proves the
// detector fires on an AuthorizeAs reference outside the allowlist.
//
// Blind spot (known, by design): the scan runs over Production() scope, which
// excludes _test.go files. Test doubles legitimately call AuthorizeAs (e.g. the
// pdpauthz unit tests, accesscore authorize_as_test.go) and are NOT flagged —
// production-only enforcement is the intended boundary (the same convention as
// every Production() caller-allowlist in this suite).
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authSubjectAuthorizerPkgPath is the import path of the package declaring the
// auth.SubjectAuthorizer interface (and its AuthorizeAs method).
const authSubjectAuthorizerPkgPath = PlatformFrameworkModulePath + "/runtime/auth"

// authorizeAsMethodName is the explicit-subject seam method.
const authorizeAsMethodName = "AuthorizeAs"

// authorizeAsCallerAllowlist is the set of module-relative production files
// allowed to reference auth.SubjectAuthorizer.AuthorizeAs. Any other production
// reference is a subject-forgery funnel breach.
// NOTE: Pass.Rel reports framework files with the physical "framework/" module
// prefix stripped (logical layer path, stripFrameworkPrefix), so these keys are
// "runtime/..." not "framework/runtime/...".
var authorizeAsCallerAllowlist = map[string]struct{}{
	"runtime/certsigning/pdpauthz/pdpauthz.go":       {},
	"runtime/bootstrap/primary_authorizer_option.go": {},
}

// TestAuthzAuthorizeAsCallerFunnel01 asserts every production reference to
// auth.SubjectAuthorizer.AuthorizeAs sits in authorizeAsCallerAllowlist, and that
// each allowlist entry is live (anti-vacuity reverse self-check).
func TestAuthzAuthorizeAsCallerFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanAuthorizeAsCallers(p, observed)
	})
	for f := range authorizeAsCallerAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"AUTHZ-AUTHORIZE-AS-CALLER-FUNNEL-01: allowlist entry %q is STALE — no live "+
						"AuthorizeAs reference observed. Either the scanner regressed or the call was "+
						"removed; drop the dead allowlist entry so it cannot become a silent "+
						"subject-forgery slot.", f,
				),
			})
		}
	}
	Report(t, "AUTHZ-AUTHORIZE-AS-CALLER-FUNNEL-01", diags)
}

// scanAuthorizeAsCallers flags every reference to auth.SubjectAuthorizer.AuthorizeAs
// outside authorizeAsCallerAllowlist, recording observed allowlisted files for
// anti-vacuity.
func scanAuthorizeAsCallers(p *Pass, observed map[string]struct{}) []Diagnostic {
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
			if !isAuthorizeAsRef(p.TypesInfo, id) {
				return
			}
			if _, allowed := authorizeAsCallerAllowlist[rel]; allowed {
				observed[rel] = struct{}{}
				return
			}
			pos := p.Fset.Position(id.Pos())
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"AUTHZ-AUTHORIZE-AS-CALLER-FUNNEL-01: auth.SubjectAuthorizer.AuthorizeAs is referenced "+
						"from %s, which is not a sanctioned caller. Business code must authorize via the "+
						"principal-based Authorize / RequirePermission path; only the certsigning/pdpauthz "+
						"adapter (and the bootstrap lazy delegation feeding it) may use the explicit-subject "+
						"seam. Forging a SubjectDescriptor elsewhere bypasses the authenticated-principal gate. "+
						"If this IS a new sanctioned non-HTTP path, add it to authorizeAsCallerAllowlist with "+
						"rationale.", rel,
				),
			})
		})
	}
	return d
}

// isAuthorizeAsRef resolves an identifier USE to the
// auth.SubjectAuthorizer.AuthorizeAs interface method via go/types (info.Uses):
// matches the package-qualified call, method-value capture, and aliased forms
// (all resolve to the same *types.Func). The method is matched by name + owning
// package path; the concrete accesscore impl resolves through the interface at
// every real call site, so anchoring on the interface method covers all callers.
func isAuthorizeAsRef(info *types.Info, id *ast.Ident) bool {
	if id.Name != authorizeAsMethodName {
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != authSubjectAuthorizerPkgPath {
		return false
	}
	// Confirm it is an interface method (the SubjectAuthorizer seam), not an
	// unrelated package-level func that happens to be named AuthorizeAs.
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	return sig.Recv() != nil
}

// TestAuthzAuthorizeAsCallerFunnel01_RedFixture is the reverse self-check: the
// fixture references auth.SubjectAuthorizer.AuthorizeAs from a package that is NOT
// on the allowlist; the detector must flag it. A 0 result means the detector
// regressed.
func TestAuthzAuthorizeAsCallerFunnel01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	fixturePkg := modPath + "/tools/archtest/internal/authorizeascallerfixture"
	pattern := "./tools/archtest/internal/authorizeascallerfixture/..."

	throwaway := map[string]struct{}{}
	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg || p.TypesInfo == nil {
			return nil
		}
		found += len(scanAuthorizeAsCallers(p, throwaway))
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: detector must flag an AuthorizeAs reference outside the allowlist")
}
