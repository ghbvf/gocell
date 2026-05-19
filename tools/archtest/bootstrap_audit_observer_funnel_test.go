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
// Downstream Hard: audit.NewBootstrapAuthFailObserver's body MUST call
// audit.AppendBootstrapAuthFail. The construction path is the only legal
// way to produce a BootstrapAuthFailObserver that carries audit-chain
// semantics; the archtest below makes "build the observer but skip the
// ledger write" not expressible inside the funnel package.
//
// Upstream Medium: every production call to auth.NewBootstrapMiddleware in
// cmd/corebundle/ (non-test) must pass an observer that is either the
// direct return value of audit.NewBootstrapAuthFailObserver or an
// identifier short-declared from it in the same source file. Pure type-
// system upstream Hard would require sealing auth.BootstrapAuthFailObserver
// into a marker that only runtime/audit can construct — which would force
// runtime/auth to import runtime/audit, breaking the documented
// non-dependency. Promotion to Hard is tracked as backlog
// BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-HARD-UPGRADE-01 (cap-14).
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
	"fmt"
	"go/ast"
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
// invoke AppendBootstrapAuthFail in its body. Removing that call would let
// the funnel emit observers that satisfy callers' types but silently
// double-write nothing to the ledger.
func TestBootstrapAuditObserverFunnelDownstreamHard01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	auditPkgPath := modPath + auditPkgSuffix

	var (
		funcBody     *ast.BlockStmt
		funcLocation string
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
				funcBody = fn.Body
				funcLocation = p.Rel(file) + ":" + p.Fset.Position(fn.Pos()).String()
			})
		}
		return nil
	})

	require.NotNil(t, funcBody,
		"%s: runtime/audit must export %s as the single observer constructor",
		ruleBootstrapAuditObserverFunnelDownstreamHard01, observerFnName)

	var found bool
	EachInSubtree[ast.CallExpr](funcBody, func(call *ast.CallExpr) {
		if found {
			return
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == appendFnName {
				found = true
			}
		case *ast.SelectorExpr:
			if fun.Sel != nil && fun.Sel.Name == appendFnName {
				found = true
			}
		}
	})
	assert.True(t, found,
		"%s: %s body must invoke %s — otherwise the observer never reaches the audit ledger and downgrades back to slog-only (%s)",
		ruleBootstrapAuditObserverFunnelDownstreamHard01, observerFnName, appendFnName, funcLocation)
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
// BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-HARD-UPGRADE-01 backlog entry); the
// archtest defines the Medium ceiling reachable without that refactor.
func TestBootstrapAuditObserverFunnelUpstreamMedium01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	auditPkgPath := modPath + auditPkgSuffix
	authPkgPath := modPath + authPkgSuffix
	corebundlePkgPath := modPath + corebundlePkgSuffix

	type violation struct {
		location string
		reason   string
	}
	var violations []violation

	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != corebundlePkgPath {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			observerVarNames := observerVarNamesInFile(p, file, auditPkgPath)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
				if !ok || pkgPath != authPkgPath || name != bootstrapMiddlewareName {
					return
				}
				if len(call.Args) < 3 {
					violations = append(violations, violation{
						location: rel + ":" + p.Fset.Position(call.Pos()).String(),
						reason:   "fewer than 3 positional args; expected (creds, limiter, observer)",
					})
					return
				}
				arg := call.Args[2]
				if isAuditObserverConstructorCall(p, arg, auditPkgPath) {
					return
				}
				if id, ok := arg.(*ast.Ident); ok && observerVarNames[id.Name] {
					return
				}
				violations = append(violations, violation{
					location: rel + ":" + p.Fset.Position(arg.Pos()).String(),
					reason:   "third arg must be audit.NewBootstrapAuthFailObserver(...) or a local ident short-declared from it",
				})
			})
		}
		return nil
	})

	for _, v := range violations {
		t.Logf("%s: %s — %s", ruleBootstrapAuditObserverFunnelUpstreamMedium01, v.location, v.reason)
	}
	assert.Empty(t, violations,
		"%s: cmd/corebundle production callers of auth.%s must route through audit.%s; "+
			"recovering to slog-only observers (the pre-PR shape) is what this rule prevents",
		ruleBootstrapAuditObserverFunnelUpstreamMedium01, bootstrapMiddlewareName, observerFnName)
}

// TestBootstrapAuditObserverFunnelUpstreamMedium01_ReverseBlindSpot validates
// the documented Medium ceiling of BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01.
//
// Blind spot: the upstream Medium check (TestBootstrapAuditObserverFunnelUpstreamMedium01)
// would be fooled if a production file in cmd/corebundle contained an AssignStmt
// of the form:
//
//	bootstrapAuthObserver := func(ctx context.Context, reason string) { /* slog-only */ }
//
// where the identifier name matched one registered by observerVarNamesInFile —
// which only registers idents short-declared from audit.NewBootstrapAuthFailObserver.
// In practice this blind-spot shape is distinct: an ident registered via
// observerVarNamesInFile always has an audit.NewBootstrapAuthFailObserver RHS,
// never a FuncLit. The reverse-blindspot test asserts no such FuncLit assignment
// to a BootstrapAuthFailObserver-typed variable exists in production code,
// confirming the shape is absent rather than just undetected.
//
// Implementation: scan cmd/corebundle non-test files for AssignStmt whose RHS
// is a FuncLit with signature func(context.Context, string) — the raw shape of
// auth.BootstrapAuthFailObserver — and assert none exist.
func TestBootstrapAuditObserverFunnelUpstreamMedium01_ReverseBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	corebundlePkgPath := modPath + corebundlePkgSuffix

	type blindspot struct {
		location string
	}
	var found []blindspot

	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != corebundlePkgPath {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.AssignStmt](file, func(stmt *ast.AssignStmt) {
				if len(stmt.Rhs) != 1 {
					return
				}
				fl, ok := stmt.Rhs[0].(*ast.FuncLit)
				if !ok {
					return
				}
				// Check for func(context.Context, string) signature — the raw
				// shape of auth.BootstrapAuthFailObserver.
				ft := fl.Type
				if ft.Params == nil || len(ft.Params.List) != 2 {
					return
				}
				// Second param must be a plain identifier (string type).
				second := ft.Params.List[1]
				if len(second.Names) == 0 {
					return
				}
				// First param must mention "Context" somewhere — loose check
				// sufficient for the blind-spot shape (formal params in go source).
				firstExpr := fmt.Sprintf("%T", ft.Params.List[0].Type)
				if !strings.Contains(firstExpr, "SelectorExpr") && !strings.Contains(firstExpr, "Ident") {
					return
				}
				found = append(found, blindspot{
					location: rel + ":" + p.Fset.Position(stmt.Pos()).String(),
				})
			})
		}
		return nil
	})

	// The blind-spot shape must NOT exist in production cmd/corebundle files.
	// If it does, BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01 could be
	// fooled. Zero occurrences = the reverse-blindspot is confirmed absent.
	assert.Empty(t, found,
		"BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01 blind-spot: "+
			"cmd/corebundle production files must not assign a FuncLit with "+
			"BootstrapAuthFailObserver signature — use audit.NewBootstrapAuthFailObserver instead; "+
			"found: %v", found)
}

// observerVarNamesInFile returns the set of identifier names short-declared
// from a CallExpr to audit.NewBootstrapAuthFailObserver anywhere in file.
// Single-pass AST walk; deliberately file-scoped to avoid cross-file leakage.
func observerVarNamesInFile(p *Pass, file *ast.File, auditPkgPath string) map[string]bool {
	out := map[string]bool{}
	EachInSubtree[ast.AssignStmt](file, func(stmt *ast.AssignStmt) {
		if len(stmt.Rhs) != 1 {
			return
		}
		if !isAuditObserverConstructorCall(p, stmt.Rhs[0], auditPkgPath) {
			return
		}
		// LHS is the observer identifier; the second LHS (when present) is the
		// error variable — both error-first and single-return forms map the
		// observer to the first LHS position.
		if len(stmt.Lhs) == 0 {
			return
		}
		if id, ok := stmt.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
			out[id.Name] = true
		}
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
