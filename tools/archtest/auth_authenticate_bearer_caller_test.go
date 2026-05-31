// auth_authenticate_bearer_caller_test.go — closes the DOWNSTREAM side of the
// AuthenticateBearer principal-injection entry point.
//
//   - INVARIANT: AUTH-AUTHENTICATE-BEARER-CALLER-01
//
// # What this guards
//
// runtime/auth.AuthenticateBearer is the single transport-agnostic
// authentication core: it verifies a bearer token and, on success, injects the
// verified *Principal into ctx (WithPrincipal + injectPrincipalCtxKeys, the
// producer-side half of the principal-propagation contract that outbox.NewEntry
// reads at its single injection trust boundary). The underlying ctxkeys setters
// (WithActorID/SubjectID/SessionID) are already locked to middleware.go by
// CTXKEYS-PRINCIPAL-WRITE-CALLER-01, and AuthenticateBearer's writes stay
// physically inside that same file — so the low-level write lock is intact.
//
// This PR EXPORTED AuthenticateBearer so the gRPC unary auth interceptor can
// share the verify→principal→ctx bridge with the HTTP middleware. Exporting a
// function that injects a verified principal creates a NEW, higher-level entry
// point: a caller that reaches AuthenticateBearer obtains a ctx stamped with an
// authenticated identity. That is exactly the trust boundary — a *Principal can
// only be produced from a token the verifier accepts (there is no claims-input
// variant, so no caller can forge a principal by fabricating Claims), but the
// SET of code paths that may stand AT the boundary and mint an authenticated ctx
// must stay enumerated. A stray business caller invoking AuthenticateBearer
// would be authenticating requests outside the two sanctioned transport edges,
// which is an authentication-surface expansion that must be reviewed, not
// silently allowed.
//
// This archtest pins the production callsite identity of AuthenticateBearer to
// the two transport request-boundary bridges:
//
//   - runtime/auth/middleware.go — handleAuthRequest, the HTTP AuthMiddleware
//     verify→principal→ctx bridge. NOTE this is a SAME-PACKAGE caller (it lives
//     in package runtime/auth), so it references AuthenticateBearer as a BARE
//     identifier, not a qualified selector — the rule resolves both forms (see
//     "Detection" below).
//   - runtime/grpc/interceptor/auth.go — UnaryAuth, the gRPC unary-interceptor
//     bridge. This is a CROSS-PACKAGE caller (auth.AuthenticateBearer selector).
//
// No producer (cells/* or examples/*) and no other runtime/adapter site may
// call it.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: MEDIUM by archtest caller-allowlist. Every reference is
//     resolved via go/types (the package's TypesInfo.Uses map → *types.Func
//     object identity), so import aliases, function-value references, AND
//     same-package bare-identifier calls all resolve to the same function — there
//     is no "looks like but isn't" gap. Any callsite outside the allowlist fails
//     in CI. This is the strongest static form available short of sealing, which
//     is impossible here (see upstream).
//   - Upstream: HARD is UNREACHABLE — a GO-LANGUAGE CEILING, not a deferred
//     TODO. AuthenticateBearer is an EXPORTED function that BOTH transport
//     bridges in DIFFERENT packages (runtime/auth, runtime/grpc/interceptor)
//     must call; Go visibility cannot express "only these two packages may call
//     this exported func". Same permanent ceiling as
//     CTXKEYS-PRINCIPAL-WRITE-CALLER-01 / OUTBOX-RECONSTRUCTION-CALLER-01 (#1282)
//     and SPAN-SETATTR-HOLDER-SEAL (#851). Tracked as a won't-do hard-upstream
//     item under gh #1394; the downstream archtest is the enforcement.
//
// # Detection iterates TypesInfo.Uses; covers selector AND same-package bare-ident
//
// The rule iterates the package's TypesInfo.Uses map directly (every use-site
// *ast.Ident → the *types.Object it resolves to) rather than walking the AST for
// a specific expression shape. This is load-bearing: the same-package caller
// middleware.go references AuthenticateBearer as a BARE identifier (no package
// qualifier), and the framework subtree walker [EachInSubtree][ast.Ident] does
// NOT surface that bare leaf ident nested inside the call statement — a verified
// SelectorExpr-only OR EachInSubtree[ast.Ident] scan both MISS the same-package
// call and trip the anti-vacuity STALE guard on middleware.go. Iterating
// TypesInfo.Uses sidesteps the walker entirely and uniformly captures BOTH AST
// shapes:
//
//   - Qualified selector `auth.AuthenticateBearer` — the cross-package caller and
//     function-value references; the selector's `.Sel` ident is a key in Uses.
//   - Bare identifier `AuthenticateBearer` — the same-package caller
//     middleware.go; the call's `.Fun` ident is a key in Uses.
//
// The function's own DECLARATION ident lives in info.Defs (NOT Uses), so it is
// never counted as a caller.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Dot-import bare-identifier form FROM ANOTHER PACKAGE (import . "…/runtime/
//     auth"; AuthenticateBearer(...)) ALSO appears in that package's
//     TypesInfo.Uses resolving to the same *types.Func, so it IS matched (flagged
//     as a non-allowlisted caller). Iterating Uses removes the dot-import blind
//     spot a pure SelectorExpr scanner would have.
//   - A call added in a //go:build-gated PRODUCTION file under a non-default tag
//     would be missed by the default-tags scan. The two bridges today are
//     default-build; documented.
//   - A use-site whose source file is not one of the Pass's own Files (Uses can
//     in principle carry idents from embedded positions) is skipped via the
//     abs→rel index; in practice Uses is keyed by this package's own idents.
//   - The anti-vacuity guard (every allowlisted file must reference the symbol
//     ≥1×) is the reverse self-check: it proves the rule resolves the real
//     references (selector + bare-ident) and forbids stale-allowlist rot. An
//     earlier AST-shape scanner tripped this guard on middleware.go — which is
//     precisely how the same-package bare-ident gap was discovered and fixed.
package archtest

import (
	"fmt"
	"go/types"
	"sort"
	"testing"
)

// authPkgPath is the import path of the package owning AuthenticateBearer.
const authPkgPath = "github.com/ghbvf/gocell/runtime/auth"

// authenticateBearerCallerAllowlist is the set of production files allowed to
// reference runtime/auth.AuthenticateBearer — the two transport request-boundary
// bridges (HTTP + gRPC). middleware.go is a same-package (bare-ident) caller;
// interceptor/auth.go is a cross-package (selector) caller.
var authenticateBearerCallerAllowlist = map[string]struct{}{
	"runtime/auth/middleware.go":       {}, // HTTP handleAuthRequest bridge (same-package bare ident)
	"runtime/grpc/interceptor/auth.go": {}, // gRPC UnaryAuth bridge (cross-package selector)
}

// TestAuthAuthenticateBearerCaller01 asserts that every production reference to
// runtime/auth.AuthenticateBearer sits in the caller allowlist, and that no
// allowlist entry is stale (anti-vacuity reverse check).
func TestAuthAuthenticateBearerCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := RunTypedProduction(t, TypedOpts{}, func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		// abs-filename → module-relative path for this Pass's files, so a Uses
		// ident can be attributed to its owning source file.
		relByAbs := make(map[string]string, len(p.Files))
		for _, f := range p.Files {
			relByAbs[p.Abs(f)] = p.Rel(f)
		}

		var d []Diagnostic
		for id, obj := range p.TypesInfo.Uses {
			if id.Name != "AuthenticateBearer" || !isAuthenticateBearerFunc(obj) {
				continue
			}
			rel, ok := relByAbs[p.Fset.Position(id.Pos()).Filename]
			if !ok {
				continue
			}
			observed[rel] = struct{}{}
			if _, allowed := authenticateBearerCallerAllowlist[rel]; !allowed {
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(id.Pos()).Line,
					Message: fmt.Sprintf(
						"AUTH-AUTHENTICATE-BEARER-CALLER-01: runtime/auth.AuthenticateBearer is referenced "+
							"from %s, which is not a sanctioned transport authentication bridge. This function "+
							"mints a ctx carrying a VERIFIED authenticated principal; only the HTTP middleware "+
							"(runtime/auth) and the gRPC unary auth interceptor (runtime/grpc/interceptor) may "+
							"stand at that trust boundary. Producers and other sites MUST NOT authenticate "+
							"requests directly. If this IS a new sanctioned transport bridge, add it to "+
							"authenticateBearerCallerAllowlist with rationale.",
						rel),
				})
			}
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check.
	allowed := make([]string, 0, len(authenticateBearerCallerAllowlist))
	for f := range authenticateBearerCallerAllowlist {
		allowed = append(allowed, f)
	}
	sort.Strings(allowed)
	for _, f := range allowed {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"AUTH-AUTHENTICATE-BEARER-CALLER-01: allowlist entry %q is STALE — no live "+
						"runtime/auth.AuthenticateBearer reference observed. Either the scanner regressed or the "+
						"call was removed; drop the dead allowlist entry so it cannot become a silent bypass slot.",
					f),
			})
		}
	}

	Report(t, "AUTH-AUTHENTICATE-BEARER-CALLER-01", diags)
}

// isAuthenticateBearerFunc reports whether obj is the package-level
// runtime/auth.AuthenticateBearer function (no receiver), so an unrelated method
// named AuthenticateBearer on some other type cannot match.
func isAuthenticateBearerFunc(obj types.Object) bool {
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
