//go:build archtest

// tenant_applyscope_write_caller_test.go — closes the DOWNSTREAM side of the
// PR-3b MID-TX tenant-scope write boundary (#1617 review F3).
//
//   - INVARIANT: TENANT-APPLYSCOPE-WRITE-CALLER-01
//
// # What this guards
//
// TENANT-TXSCOPE-WRITE-CALLER-01 already pins tenant.WithScope (the scope-at-tx-
// START path used by scopedtx.Do / configcore scopedread). PR-3b added a SECOND
// way to write the RLS app.tenant_id GUC: the MID-TX path, for sessionrefresh's
// cross-store tx that can only learn the tenant after reading sessions.tenant_id
// inside the tx. That path does NOT go through tenant.WithScope — it calls
// persistence.CellTxManager.ApplyTenantScope, which forwards (via scopedtx.ApplyScope)
// to adapters/postgres.TxManager.ApplyTenantScope → writeTenantGUC. So WithScope's
// caller-allowlist did not cover it, leaving a mid-tx GUC-write entry point that
// any cell holding a CellTxManager could call to scope a transaction to an
// arbitrary tenant — the same cross-tenant-read threat WithScope is funneled
// against.
//
// This archtest pins both rungs of that mid-tx ladder to their sole sanctioned
// callers:
//
//   - persistence.CellTxManager.ApplyTenantScope (the kernel interface method) —
//     only cells/accesscore/internal/scopedtx/scopedtx.go (the ApplyScope helper).
//   - cells/accesscore/internal/scopedtx.ApplyScope (the accesscore helper) —
//     only cells/accesscore/slices/sessionrefresh/service.go (the one cross-store
//     tx that scopes mid-flight; every other accesscore site scopes at tx-start
//     via scopedtx.Do).
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
// The two axes are separate:
//
//   - Downstream (who may CALL the mid-tx scope-write): HARD. Both rungs resolve
//     every identifier USE to the target *types.Func via go/types (info.Uses).
//     The method rung additionally binds the receiver type to persistence.CellTxManager
//     (methodRecvTypeName), so the structurally-identical local `tenantScoper`
//     delegation inside kernel/persistence/cell_marker.go and the concrete
//     impls (internalCellTxManager / adapters/postgres.TxManager) are excluded —
//     only the interface-method call is matched. Import alias is irrelevant: a
//     method call carries no package qualifier (the method binds to the receiver
//     type), and the free-func rung resolves through Uses like WithScope.
//   - Upstream (can the GUC be written WITHOUT this method): MEDIUM. ApplyTenantScope
//     is a public method on a sealed interface that every cell's txRunner holds,
//     so Go cannot make a non-sanctioned holder's call unrepresentable — the
//     archtest caller-allowlist is the enforcement. The physical GUC write is
//     still funneled to tx_manager.go::writeTenantGUC by PG-SETLOCAL-FUNNEL-01;
//     the type-system Hard upgrade (a sealed GUC-write handle that also closes
//     the setLocalTenant path) is the SAME ceiling that funnel tracks, won't-do
//     at **gh #1619** (Go-visibility family, with SPAN-SETATTR-HOLDER-SEAL #851 /
//     HEALTHZ-HOLDER-SEAL #893 / outbox principal-write #1282). The scopedtx.ApplyScope
//     rung has a TIGHTER natural bound: it lives in cells/accesscore/internal, so
//     no package outside cells/accesscore can even reference it (Go internal-import
//     visibility — a compile error, not an archtest finding).
//
// # Detection + anti-vacuity
//
// Use-based (info.Uses), so each rung matches whether called or passed as a value.
// The anti-vacuity reverse check requires each allowlisted file to actually
// reference its target at least once, so a scanner regression or a removed call
// (which would make the funnel vacuously pass) fails CI. The method rung carries
// a RED fixture (internal/applyscopefixture, exercised by
// TestTenantApplyScopeWriteCaller01_FixtureCatchesMethodCall) proving the detector
// fires on an out-of-allowlist CellTxManager.ApplyTenantScope call — without it
// the production scan is non-vacuous only via the anti-vacuity reverse check, but
// not proven to flag a genuine violation.
//
// # Tool blind spot (charter §"强制盲区自检")
//
//   - A scope write assembled by reflection, or otherwise without a compile-time
//     reference to ApplyTenantScope / ApplyScope, is invisible — the same known
//     limit as every identifier-resolution funnel in this suite.
//   - The free-func rung (scopedtx.ApplyScope) has no external RED fixture: an
//     internal package is not importable from tools/archtest, which IS the
//     upstream bound for that rung (no external caller can be constructed).
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/require"
)

// persistencePkgPath is declared in aftercommit_pure_transient_invariants.go and
// sessionrefreshPkgPath in sessionrefresh_no_session_create_test.go (same
// package); reused here.

// applyTenantScopeMethod is the kernel mid-tx GUC-write interface method.
const applyTenantScopeMethod = "ApplyTenantScope"

// scopedtxApplyScopeFunc is the accesscore mid-flight scope helper function.
const scopedtxApplyScopeFunc = "ApplyScope"

// cellTxManagerTypeName is the receiver interface that owns ApplyTenantScope; the
// method rung binds to it so the local `tenantScoper` delegation and the concrete
// impls in the same / other packages are excluded.
const cellTxManagerTypeName = "CellTxManager"

// scopedtxPkgPath is the accesscore internal scope funnel package.
const scopedtxPkgPath = PlatformModulePath + "/cells/accesscore/internal/scopedtx"

// applyTenantScopeMethodCallerAllowlist: the sole sanctioned caller of the kernel
// CellTxManager.ApplyTenantScope interface method.
var applyTenantScopeMethodCallerAllowlist = map[string]struct{}{
	"cells/accesscore/internal/scopedtx/scopedtx.go": {},
}

// scopedtxApplyScopeCallerAllowlist: the sole sanctioned caller of scopedtx.ApplyScope
// (mid-flight scoping is only legitimate in the sessionrefresh cross-store tx).
var scopedtxApplyScopeCallerAllowlist = map[string]struct{}{
	"cells/accesscore/slices/sessionrefresh/service.go": {},
}

// TestTenantApplyScopeWriteCaller01 asserts every production reference to the
// kernel CellTxManager.ApplyTenantScope method and to scopedtx.ApplyScope sits in
// its allowlist, and that the allowlist entries are live (anti-vacuity).
func TestTenantApplyScopeWriteCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	methodObserved := map[string]struct{}{}
	funcObserved := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				switch {
				case isApplyTenantScopeMethodRef(p.TypesInfo, id):
					methodObserved[rel] = struct{}{}
					if _, allowed := applyTenantScopeMethodCallerAllowlist[rel]; !allowed {
						pos := p.Fset.Position(id.Pos())
						d = append(d, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"TENANT-APPLYSCOPE-WRITE-CALLER-01: persistence.CellTxManager.ApplyTenantScope is "+
									"referenced from %s, which is not a sanctioned mid-tx scope writer. It writes the RLS "+
									"app.tenant_id GUC mid-transaction, so it is the same tenant-isolation boundary as "+
									"tenant.WithScope. Business code must scope at tx-start via scopedtx.Do (which the "+
									"authenticated principal feeds), not scope an arbitrary tenant mid-flight. If this IS a "+
									"new sanctioned writer, add it to applyTenantScopeMethodCallerAllowlist with rationale.",
								rel,
							),
						})
					}
				case isScopedtxApplyScopeRef(p.TypesInfo, id):
					funcObserved[rel] = struct{}{}
					if _, allowed := scopedtxApplyScopeCallerAllowlist[rel]; !allowed {
						pos := p.Fset.Position(id.Pos())
						d = append(d, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"TENANT-APPLYSCOPE-WRITE-CALLER-01: scopedtx.ApplyScope is referenced from %s, which is "+
									"not the sanctioned mid-flight scope caller. Mid-flight scoping (scope-AFTER-reading a "+
									"non-RLS carrier inside the tx) is only legitimate for the sessionrefresh cross-store tx; "+
									"every other accesscore site must scope at tx-start via scopedtx.Do. If this IS a new "+
									"sanctioned mid-flight caller, add it to scopedtxApplyScopeCallerAllowlist with rationale.",
								rel,
							),
						})
					}
				}
			})
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check for both rungs.
	for f := range applyTenantScopeMethodCallerAllowlist {
		if _, seen := methodObserved[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"TENANT-APPLYSCOPE-WRITE-CALLER-01: method allowlist entry %q is STALE — no live "+
						"CellTxManager.ApplyTenantScope reference observed. Either the scanner regressed or the call "+
						"was removed; drop the dead allowlist entry so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}
	for f := range scopedtxApplyScopeCallerAllowlist {
		if _, seen := funcObserved[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"TENANT-APPLYSCOPE-WRITE-CALLER-01: func allowlist entry %q is STALE — no live scopedtx.ApplyScope "+
						"reference observed. Either the scanner regressed or the call was removed; drop the dead "+
						"allowlist entry so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "TENANT-APPLYSCOPE-WRITE-CALLER-01", diags)
}

// TestTenantApplyScopeWriteCaller01_FixtureCatchesMethodCall is the reverse
// self-check for the method rung: the RED fixture calls
// persistence.CellTxManager.ApplyTenantScope from a non-allowlisted file. The
// Use-based detector (binding the receiver type to CellTxManager) must resolve
// and flag it. A 0 result means the detector regressed (e.g. lost the receiver
// binding and now also matches the local tenantScoper delegation, or stopped
// resolving the method at all), reopening the mid-tx GUC write as a silent bypass.
func TestTenantApplyScopeWriteCaller01_FixtureCatchesMethodCall(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkg := modPath + "/tools/archtest/internal/applyscopefixture"
	pattern := "./tools/archtest/internal/applyscopefixture/..."
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg || p.TypesInfo == nil {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isApplyTenantScopeMethodRef(p.TypesInfo, id) {
					return
				}
				pos := p.Fset.Position(id.Pos())
				d = append(d, Diagnostic{Rel: rel, Line: pos.Line, Message: "CellTxManager.ApplyTenantScope reference"})
			})
		}
		return d
	})
	for _, dd := range diags {
		t.Log(dd.Message)
	}
	require.Len(t, diags, 1,
		"the Use-based detector must resolve the out-of-allowlist CellTxManager.ApplyTenantScope call; "+
			"a 0 result means it regressed and the mid-tx GUC write is a bypass")
}

// isApplyTenantScopeMethodRef resolves an identifier USE to the
// persistence.CellTxManager.ApplyTenantScope interface method via go/types
// (info.Uses), binding the receiver type to CellTxManager. The receiver binding
// is what distinguishes the interface-method call (scopedtx.go) from the
// structurally-identical local `tenantScoper` delegation inside
// kernel/persistence/cell_marker.go and from the concrete impls
// (internalCellTxManager / adapters/postgres.TxManager.ApplyTenantScope), all of
// which share the same method name.
func isApplyTenantScopeMethodRef(info *types.Info, id *ast.Ident) bool {
	fn, ok := info.Uses[id].(*types.Func)
	if !ok {
		return false
	}
	return fn.Name() == applyTenantScopeMethod &&
		fn.Pkg() != nil && fn.Pkg().Path() == persistencePkgPath &&
		methodRecvTypeName(fn) == cellTxManagerTypeName
}

// isScopedtxApplyScopeRef resolves an identifier USE to the free function
// cells/accesscore/internal/scopedtx.ApplyScope via go/types (info.Uses),
// matching every import form (qualified / alias / dot-import all resolve to the
// same *types.Func), like the WithScope detector.
func isScopedtxApplyScopeRef(info *types.Info, id *ast.Ident) bool {
	fn, ok := info.Uses[id].(*types.Func)
	if !ok {
		return false
	}
	return fn.Name() == scopedtxApplyScopeFunc &&
		fn.Pkg() != nil && fn.Pkg().Path() == scopedtxPkgPath
}

// methodRecvTypeName returns the unqualified name of fn's receiver named type
// (pointer-stripped), or "" when fn is not a method or its receiver is an
// unnamed type. For an interface method obtained via info.Uses, the receiver is
// the enclosing named interface (e.g. CellTxManager).
func methodRecvTypeName(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return ""
	}
	recv := sig.Recv()
	if recv == nil {
		return ""
	}
	t := recv.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return ""
}
