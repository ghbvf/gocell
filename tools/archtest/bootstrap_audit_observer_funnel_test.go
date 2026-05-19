// invariants:
//   - INVARIANT: BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01
//   - INVARIANT: BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01
//
// Funnel double-lock for BOOTSTRAP-AUDIT-CHAIN-WIRING-01 (plan 039 W1-2).
// runtime/auth.NewBootstrapMiddleware accepts a third positional argument of
// type auth.BootstrapAuthFailObserver (= func(ctx, reason)). Both the
// bootstrap-period slog channel AND the audit hash-chain channel must fire
// for every failure event; relying on a freestyle func literal here is what
// the prior bootstrapAuthFailLogger did and how the audit channel was lost.
//
// Downstream Hard: audit.NewBootstrapAuthFailObserver's RETURNED CLOSURE
// MUST call audit.AppendBootstrapAuthFail (typed-resolved via ResolvePackageRef
// to the canonical runtime/audit package, not a same-named selector). The
// construction path is the only legal way to produce a
// BootstrapAuthFailObserver that carries audit-chain semantics; scanning
// only the returned FuncLit body (not the constructor body at large)
// closes the dead-code / outside-closure-path bypass — "build the observer
// but skip the ledger write" is not expressible inside the funnel package.
//
// Upstream Medium (Object-bound + reassign-scanned): every production call to
// auth.NewBootstrapMiddleware in cmd/corebundle/ (non-test) must pass an
// observer that is either (a) the direct return value of a CallExpr resolving
// (via ResolvePackageRef) to runtime/audit.NewBootstrapAuthFailObserver, or
// (b) a *types.Object short-declared (:=) from such a CallExpr in the same
// source file AND never subsequently reassigned (=) to a non-funnel RHS.
// Tracking by types.Object — not bare ident name — defeats same-name shadow
// in another scope; the reassign scan defeats `obs = func(...){slog only}`
// shape that an earlier name-only check would miss.
//
// Wrapper-chain blind spot remains (e.g. `obs2 := wrap(obs1)` where wrap
// returns a custom impl): this is reachable only via Go-type-system upstream
// Hard (sealing auth.BootstrapAuthFailObserver into a marker that only
// runtime/audit can construct, which forces runtime/auth → runtime/audit,
// breaking the documented non-dependency). Promotion to Hard is tracked as
// backlog BOOTSTRAP-AUDIT-OBSERVER-RUNTIME-AUTH-WINDOW-01 (arm a: sealed
// marker, cap-14). This archtest defines the Medium ceiling reachable
// without that refactor.
//
// Scope carve-out (must match the F2 plan decision):
//   - examples/ssobff/app.go intentionally still wires the legacy
//     slog-only observer; backlog SSOBFF-BOOTSTRAP-AUDIT-CHAIN-WIRING-01
//     will move it through this same funnel and widen the scope below.
//   - cmd/corebundle/*_test.go invocations are mocks for rate-limit /
//     bootstrap-credential paths and do not represent production wiring;
//     RunTypedProduction(opts.Tests=false) keeps them out of the Pass set.

package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleBootstrapAuditObserverFunnelDownstreamHard01 = "BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-DOWNSTREAM-HARD-01"
	ruleBootstrapAuditObserverFunnelUpstreamMedium01 = "BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01"

	auditPkgSuffix          = "/runtime/audit"
	authPkgSuffix           = "/runtime/auth"
	corebundlePkgSuffix     = "/cmd/corebundle"
	observerFnName          = "NewBootstrapAuthFailObserver"
	appendFnName            = "AppendBootstrapAuthFail"
	bootstrapMiddlewareName = "NewBootstrapMiddleware"
)

// TestBootstrapAuditObserverFunnelDownstreamHard01 enforces the downstream
// half of the funnel: inside runtime/audit, the only constructor that
// produces a BootstrapAuthFailObserver — NewBootstrapAuthFailObserver — must
// (a) return a FuncLit closure as its observer value, and (b) call the
// typed runtime/audit.AppendBootstrapAuthFail inside that closure's body.
// Removing the call, hiding it in dead code outside the returned closure,
// or shadowing it with a same-named selector from another package would
// let the funnel emit observers that satisfy callers' types but silently
// double-write nothing to the ledger.
//
// Hard form: (typed callee resolves to runtime/audit.AppendBootstrapAuthFail
// via ResolvePackageRef) AND (scope = returned FuncLit body, not the
// constructor body at large). Both must hold simultaneously — any other
// shape fails archtest.
func TestBootstrapAuditObserverFunnelDownstreamHard01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	auditPkgPath := modPath + auditPkgSuffix

	var (
		constructorBody *ast.BlockStmt
		funcLocation    string
		passRef         *Pass
	)
	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != auditPkgPath {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Name.Name != observerFnName {
					return
				}
				constructorBody = fn.Body
				funcLocation = p.Rel(file) + ":" + p.Fset.Position(fn.Pos()).String()
				passRef = p
			})
		}
		return nil
	})

	require.NotNil(t, constructorBody,
		"%s: runtime/audit must export %s as the single observer constructor",
		ruleBootstrapAuditObserverFunnelDownstreamHard01, observerFnName)
	require.NotNil(t, passRef, "%s: typed Pass must be captured", ruleBootstrapAuditObserverFunnelDownstreamHard01)

	// Find the returned FuncLit. The Hard form requires the AppendBootstrapAuthFail
	// call to live inside the closure that is actually handed back to callers,
	// not anywhere else in the constructor body (where dead code or sibling
	// helpers could fake compliance with the simpler "body contains call" rule).
	var returnedClosureBody *ast.BlockStmt
	EachInSubtree[ast.ReturnStmt](constructorBody, func(ret *ast.ReturnStmt) {
		if returnedClosureBody != nil {
			return
		}
		for _, result := range ret.Results {
			if fl, ok := result.(*ast.FuncLit); ok {
				returnedClosureBody = fl.Body
				return
			}
		}
	})
	require.NotNil(t, returnedClosureBody,
		"%s: %s must return a FuncLit closure as its observer; non-FuncLit return shape at %s "+
			"would break the closure-body scope this Hard rule depends on",
		ruleBootstrapAuditObserverFunnelDownstreamHard01, observerFnName, funcLocation)

	// Typed callee resolution: callee must resolve to the canonical
	// runtime/audit.AppendBootstrapAuthFail (both bare-ident same-package
	// calls and external SelectorExpr qualified calls work via ResolvePackageRef).
	// Same-named selectors from foreign packages or unrelated function-typed
	// values will NOT match.
	var found bool
	EachInSubtree[ast.CallExpr](returnedClosureBody, func(call *ast.CallExpr) {
		if found {
			return
		}
		pkgPath, name, ok := ResolvePackageRef(passRef.TypesInfo, call.Fun)
		if !ok {
			return
		}
		if pkgPath == auditPkgPath && name == appendFnName {
			found = true
		}
	})
	assert.True(t, found,
		"%s: returned closure body of %s must invoke typed %s.%s — "+
			"otherwise the observer never reaches the audit ledger and "+
			"downgrades back to slog-only (%s)",
		ruleBootstrapAuditObserverFunnelDownstreamHard01,
		observerFnName, auditPkgPath, appendFnName, funcLocation)
}

// TestBootstrapAuditObserverFunnelUpstreamMedium01 enforces the upstream
// half: every production call to auth.NewBootstrapMiddleware in cmd/corebundle
// must hand the funnel-built observer as its third positional argument. The
// observer may be either the direct CallExpr to
// audit.NewBootstrapAuthFailObserver or an identifier short-declared from it
// in the same source file.
//
// Why a small Medium gap exists: a function-level "X = func(ctx, reason){...}"
// assignment with the same identifier as a legitimate funnel-built observer
// could in principle smuggle a slog-only impl past this check. Hardening
// requires sealing auth.BootstrapAuthFailObserver (see
// BOOTSTRAP-AUDIT-OBSERVER-RUNTIME-AUTH-WINDOW-01 arm a backlog entry); the
// archtest defines the Medium ceiling reachable without that refactor.
func TestBootstrapAuditObserverFunnelUpstreamMedium01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	auditPkgPath := modPath + auditPkgSuffix
	authPkgPath := modPath + authPkgSuffix
	corebundlePkgPath := modPath + corebundlePkgSuffix

	var upstreamViolations []upstreamViolation

	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != corebundlePkgPath {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			observerObjects := observerObjectsInFile(p, file, auditPkgPath)
			// Reassign scan: a funnel-built object subsequently rebound to a
			// non-funnel RHS (FuncLit, wrapper-returned closure, …) loses the
			// audit-chain guarantee. The reassign violation is reported even
			// if the rebound object never reaches NewBootstrapMiddleware,
			// because the shape itself is what the funnel forbids.
			upstreamViolations = append(upstreamViolations,
				reassignViolations(p, file, observerObjects, auditPkgPath)...)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
				if !ok || pkgPath != authPkgPath || name != bootstrapMiddlewareName {
					return
				}
				if len(call.Args) < 3 {
					upstreamViolations = append(upstreamViolations, upstreamViolation{
						location: rel + ":" + p.Fset.Position(call.Pos()).String(),
						reason:   "fewer than 3 positional args; expected (creds, limiter, observer)",
					})
					return
				}
				arg := call.Args[2]
				if isAuditObserverConstructorCall(p, arg, auditPkgPath) {
					return
				}
				if id, ok := arg.(*ast.Ident); ok {
					if obj := p.TypesInfo.Uses[id]; obj != nil && observerObjects[obj] {
						return
					}
				}
				upstreamViolations = append(upstreamViolations, upstreamViolation{
					location: rel + ":" + p.Fset.Position(arg.Pos()).String(),
					reason:   "third arg must be audit.NewBootstrapAuthFailObserver(...) or a *types.Object short-declared from it (never reassigned)",
				})
			})
		}
		return nil
	})

	for _, v := range upstreamViolations {
		t.Logf("%s: %s — %s", ruleBootstrapAuditObserverFunnelUpstreamMedium01, v.location, v.reason)
	}
	assert.Empty(t, upstreamViolations,
		"%s: cmd/corebundle production callers of auth.%s must route through audit.%s; "+
			"recovering to slog-only observers (the pre-PR shape) is what this rule prevents",
		ruleBootstrapAuditObserverFunnelUpstreamMedium01, bootstrapMiddlewareName, observerFnName)
}

// upstreamViolation is the upstream Medium scan unit: location + reason for one
// archtest finding inside cmd/corebundle production files.
type upstreamViolation struct {
	location string
	reason   string
}

// observerObjectsInFile returns the set of *types.Object that were
// short-declared (:=) from a CallExpr to audit.NewBootstrapAuthFailObserver
// anywhere in file. Object-keyed (not name-keyed) so same-name shadows in
// other scopes do not contaminate the funnel-registered set.
//
// Only token.DEFINE (`:=`) registers an object; bare `=` reassignment is
// scanned separately by reassignViolations to detect funnel-built observers
// later rebound to non-funnel RHS.
func observerObjectsInFile(p *Pass, file *ast.File, auditPkgPath string) map[types.Object]bool {
	out := map[types.Object]bool{}
	EachInSubtree[ast.AssignStmt](file, func(stmt *ast.AssignStmt) {
		if stmt.Tok != token.DEFINE || len(stmt.Rhs) != 1 || len(stmt.Lhs) == 0 {
			return
		}
		if !isAuditObserverConstructorCall(p, stmt.Rhs[0], auditPkgPath) {
			return
		}
		id, ok := stmt.Lhs[0].(*ast.Ident)
		if !ok || id.Name == "_" {
			return
		}
		if obj := p.TypesInfo.Defs[id]; obj != nil {
			out[obj] = true
		}
	})
	return out
}

// reassignViolations finds AssignStmt with token.ASSIGN (bare `=`) where the
// LHS resolves to an object already registered by observerObjectsInFile AND
// the RHS is NOT another funnel call. This catches the upstream Medium
// blind-spot from earlier name-only checks:
//
//	obs, _ := audit.NewBootstrapAuthFailObserver(...) // registered
//	obs    = func(ctx, reason){ /* slog only */ }     // ← caught here
//	auth.NewBootstrapMiddleware(creds, lim, obs)       // would otherwise smuggle
//
// Reassigning a funnel observer to ANY non-funnel RHS is a upstreamViolation in
// itself, regardless of whether the rebound object reaches NewBootstrapMiddleware.
func reassignViolations(p *Pass, file *ast.File, observerObjects map[types.Object]bool, auditPkgPath string) []upstreamViolation {
	var out []upstreamViolation
	EachInSubtree[ast.AssignStmt](file, func(stmt *ast.AssignStmt) {
		if stmt.Tok != token.ASSIGN || len(stmt.Rhs) != 1 || len(stmt.Lhs) == 0 {
			return
		}
		id, ok := stmt.Lhs[0].(*ast.Ident)
		if !ok {
			return
		}
		obj := p.TypesInfo.Uses[id]
		if obj == nil || !observerObjects[obj] {
			return
		}
		if isAuditObserverConstructorCall(p, stmt.Rhs[0], auditPkgPath) {
			return
		}
		out = append(out, upstreamViolation{
			location: p.Rel(file) + ":" + p.Fset.Position(stmt.Pos()).String(),
			reason:   "funnel-built observer reassigned to non-funnel RHS (would bypass audit hash-chain)",
		})
	})
	return out
}

// isAuditObserverConstructorCall reports whether expr is a CallExpr whose
// callee resolves to audit.NewBootstrapAuthFailObserver in the canonical
// runtime/audit package — alias-resistant via typed *types.PkgName lookup.
func isAuditObserverConstructorCall(p *Pass, expr ast.Expr, auditPkgPath string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok {
		return false
	}
	return pkgPath == auditPkgPath && name == observerFnName
}
