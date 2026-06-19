//go:build archtest

// auth_withauthorizer_caller_test.go — closes the CALLER side of the
// runtime/auth.WithAuthorizer Authorizer-injection entry point.
//
//   - INVARIANT: AUTH-WITHAUTHORIZER-CALLER-01
//
// # What this guards
//
// runtime/auth.WithAuthorizer is the SOLE writer of an auth.Authorizer (the ABAC
// PDP) into a request context — the unexported authorizerKey makes any other
// write path a compile error (AUTHORIZER-CTX-FUNNEL-01, permission.go). The route
// gate auth.RequirePermission reads that ctx Authorizer at policy-eval time to
// decide allow/deny.
//
// WithAuthorizer is EXPORTED because the composition root (runtime/bootstrap)
// installs it as primary-listener default middleware (phases_http.go) — a
// cross-package call Go visibility cannot scope to bootstrap alone. An exported
// injector is a real authz-bypass surface, NOT mere hygiene:
//
//   - A cell route handler is mounted with its RequirePermission policy wrapped on
//     the INSIDE (runtime/auth.wrapMountGuards), while a RouteGroup's cell-supplied
//     Middleware wraps on the OUTSIDE (router.nativeMuxAdapter.Handle:
//     chain(handler, a.middlewares...)).
//   - So a cell's RouteGroup.Middleware executes BEFORE its own RequirePermission
//     gate reads the Authorizer. A middleware calling
//     auth.WithAuthorizer(ctx, alwaysAllowPDP) would make the gate consult the
//     forged PDP — a cell silently neutering its own declared permission gate. In
//     a zero-trust multi-cell platform the framework must be able to trust that a
//     declared gate actually gates.
//
// This archtest pins every PRODUCTION reference to runtime/auth.WithAuthorizer to
// the sanctioned composition-root + test-wiring sites:
//
//   - runtime/bootstrap/phases_http.go — authorizerInjector, the primary-listener
//     default middleware the composition root installs (the one sanctioned PDP
//     injection point).
//   - corecells/configcore/configcoretest/authz.go — the configcore ABAC-PDP
//     test-wiring helper. It is a production-scanned (non-_test.go) package that
//     re-exports the canonical funnel so slice handler tests get an Authorizer in
//     ctx without importing runtime/auth directly; its own godoc acknowledges it
//     stays production-scanned. (Slices that would import-cycle through it define
//     equivalent unexported stubs in their own _test.go files, which are outside
//     Production scope and need no entry here.)
//
// No cell (corecells/* or examples/*) and no other runtime/adapter site may call
// it; a stray caller is an authz-injection-surface expansion that must be reviewed.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Upstream (can the ctx key be written another way): HARD — the unexported
//     authorizerKey means WithAuthorizer is the only writer (AUTHORIZER-CTX-FUNNEL-01).
//   - Downstream (who may CALL WithAuthorizer): MEDIUM by archtest caller-allowlist.
//     Every reference is resolved via go/types (TypesInfo.Uses → *types.Func object
//     identity), so import aliases, function-value references, AND same-package
//     bare-identifier calls all resolve to the same function — no "looks like but
//     isn't" gap. Any callsite outside the allowlist fails CI.
//   - HARD for the caller restriction is UNREACHABLE — a GO-LANGUAGE CEILING, not a
//     deferred TODO: an exported func a DIFFERENT package (runtime/bootstrap) must
//     call cannot be visibility-scoped to one caller. Same permanent ceiling as
//     AUTH-AUTHENTICATE-BEARER-CALLER-01 / AUTHZ-DECISION-ALLOW-DENY-CALLER-01 /
//     CTXKEYS-PRINCIPAL-WRITE-CALLER-01 (#1282). The Medium caller-allowlist is the
//     enforcement.
//
// # Detection iterates TypesInfo.Uses; covers selector AND same-package bare-ident
//
// (mirrors AUTH-AUTHENTICATE-BEARER-CALLER-01) The rule iterates the package's
// TypesInfo.Uses map (every use-site ident → resolved *types.Object), capturing
// qualified selectors (auth.WithAuthorizer), aliased/value references, and any
// future same-package bare-identifier caller inside runtime/auth. The function's
// own DECLARATION ident lives in info.Defs (never Uses), so it is not counted as a
// caller. configcoretest declares its OWN package-level func also named
// WithAuthorizer; that object resolves to a *types.Func in the configcoretest
// package (not authPkgPath), so its declaration is NOT matched — only its body's
// call to auth.WithAuthorizer is.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Production() scope excludes _test.go files: test doubles that inject an
//     Authorizer via auth.WithAuthorizer (slice handler tests, local cycle-break
//     stubs) are not flagged — production-only enforcement is the intended
//     boundary, identical to every Production() caller-allowlist in this suite.
//   - A call in a //go:build-gated production file under a non-default tag is missed
//     by the default-tags scan; the two callers today are default-build.
//   - A truly EXTERNAL cell (compiled outside this repo, never scanned) is beyond
//     archtest reach; closing that residual requires reordering the route gate
//     ahead of cell RouteGroup.Middleware — a larger framework-routing change
//     tracked as a separate backlog item, not this caller funnel.
//   - Anti-vacuity: every allowlisted file must reference WithAuthorizer ≥1×
//     (no-stale reverse check), so a removed call or scanner regression fails CI
//     rather than vacuously passing. A RED fixture
//     (internal/withauthorizercallerfixture) proves the detector fires on a
//     WithAuthorizer reference outside the allowlist.
package archtest

import (
	"fmt"
	"go/types"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withAuthorizerCallerAllowlist is the set of module-relative production files
// allowed to reference runtime/auth.WithAuthorizer — the sanctioned Authorizer
// (PDP) injection sites.
var withAuthorizerCallerAllowlist = map[string]struct{}{
	"runtime/bootstrap/phases_http.go":             {}, // composition-root primary-listener PDP injector (authorizerInjector)
	"corecells/configcore/configcoretest/authz.go": {}, // configcore ABAC-PDP test-wiring seam (see godoc)
}

// TestAuthWithAuthorizerCaller01 asserts that every production reference to
// runtime/auth.WithAuthorizer sits in withAuthorizerCallerAllowlist, and that no
// allowlist entry is stale (anti-vacuity reverse check).
func TestAuthWithAuthorizerCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanWithAuthorizerCallers(p, observed)
	})

	// Anti-vacuity / no-stale reverse self-check.
	allowed := make([]string, 0, len(withAuthorizerCallerAllowlist))
	for f := range withAuthorizerCallerAllowlist {
		allowed = append(allowed, f)
	}
	sort.Strings(allowed)
	for _, f := range allowed {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"AUTH-WITHAUTHORIZER-CALLER-01: allowlist entry %q is STALE — no live "+
						"runtime/auth.WithAuthorizer reference observed. Either the scanner regressed or the "+
						"call was removed; drop the dead allowlist entry so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "AUTH-WITHAUTHORIZER-CALLER-01", diags)
}

// scanWithAuthorizerCallers flags every reference to runtime/auth.WithAuthorizer
// outside withAuthorizerCallerAllowlist, recording observed allowlisted files for
// the anti-vacuity reverse check. It iterates TypesInfo.Uses so selector,
// aliased, and same-package bare-identifier forms all resolve to the same func.
func scanWithAuthorizerCallers(p *Pass, observed map[string]struct{}) []Diagnostic {
	relByAbs := make(map[string]string, len(p.Files))
	for _, f := range p.Files {
		relByAbs[p.Abs(f)] = p.Rel(f)
	}

	var d []Diagnostic
	for id, obj := range p.TypesInfo.Uses {
		if id.Name != "WithAuthorizer" || !isWithAuthorizerFunc(obj) {
			continue
		}
		rel, ok := relByAbs[p.Fset.Position(id.Pos()).Filename]
		if !ok {
			continue
		}
		observed[rel] = struct{}{}
		if _, allowed := withAuthorizerCallerAllowlist[rel]; !allowed {
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(id.Pos()).Line,
				Message: fmt.Sprintf(
					"AUTH-WITHAUTHORIZER-CALLER-01: runtime/auth.WithAuthorizer is referenced from %s, "+
						"which is not a sanctioned Authorizer-injection site. WithAuthorizer writes the ABAC "+
						"PDP that RequirePermission consults; only the composition-root primary-listener "+
						"injector (runtime/bootstrap) and the configcore test-wiring seam may call it. A cell "+
						"calling it (e.g. in RouteGroup.Middleware, which runs BEFORE its own gate) can swap the "+
						"PDP and silently neuter its declared permission gate. If this IS a new sanctioned "+
						"injection site, add it to withAuthorizerCallerAllowlist with rationale.",
					rel,
				),
			})
		}
	}
	return d
}

// isWithAuthorizerFunc reports whether obj is the package-level
// runtime/auth.WithAuthorizer function (no receiver), so an unrelated func or
// method named WithAuthorizer in another package (e.g. configcoretest's own
// re-export wrapper declaration) cannot match.
func isWithAuthorizerFunc(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != authPkgPath {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	return sig.Recv() == nil
}

// TestAuthWithAuthorizerCaller01_RedFixture is the reverse self-check: the fixture
// references runtime/auth.WithAuthorizer from a package that is NOT on the
// allowlist, so the use-based detector must flag it. A 0 result means the detector
// regressed and the main scan would be vacuous.
func TestAuthWithAuthorizerCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	fixturePkg := modPath + "/tools/archtest/internal/withauthorizercallerfixture"
	pattern := "./tools/archtest/internal/withauthorizercallerfixture/..."

	throwaway := map[string]struct{}{}
	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg || p.TypesInfo == nil {
			return nil
		}
		found += len(scanWithAuthorizerCallers(p, throwaway))
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: detector must flag a runtime/auth.WithAuthorizer reference outside the allowlist")
}
