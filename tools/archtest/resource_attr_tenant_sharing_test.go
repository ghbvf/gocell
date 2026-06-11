//go:build archtest

// resource_attr_tenant_sharing_test.go — guards the tenant-sharing structural
// guarantee for the ABAC PIP (Policy Information Point) resource-attribute fetch.
//
//   - INVARIANT: RESOURCE-ATTR-TENANT-SHARING-01 (Medium)
//
// # What this guards
//
// ports.ResourceAttributeProvider.GetAttributes fetches resource attributes for
// ABAC condition evaluation. Its tenant parameter MUST be the same ctx-derived
// tenant as the policy load so that both reads are tenant-consistent — the
// structural guarantee is that they share one scopedtx.Do block bound to a
// single TenantID. Allowing any arbitrary caller to invoke GetAttributes breaks
// that structural guarantee: a caller outside the single sanctioned site could
// supply a different tenant, cross-tenant bleed, or omit the tenant binding
// entirely.
//
// This archtest pins GetAttributes to its sole sanctioned caller:
//
//   - corecells/accesscore/slices/authorizationdecide/service.go — the one site
//     where policy load and resource-attribute fetch are co-located inside a
//     single scopedtx.Do(ctx, s.txRunner, tid, …) closure.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream (who may CALL GetAttributes): MEDIUM. GetAttributes is a public
//     interface method on ports.ResourceAttributeProvider, which every holder
//     (cell, test, composition root) could call. Go cannot make a non-sanctioned
//     call unrepresentable at the type-system level — the archtest caller-allowlist
//     is the enforcement. Hard-by-construction (dropping the tenant param or
//     making the method package-private) was rejected: it would diverge from the
//     route-A explicit-typed-param funnel that the EPIC (#1347) standardised on
//     for ALL port interfaces (PolicyRepository, ResourceAttributeProvider). See
//     TENANT-REPO-PARAM-FUNNEL-01 for the upstream half. This archtest is the
//     downstream enforcement.
//   - Upstream (can GetAttributes be called WITHOUT the interface method):
//     MEDIUM. A concrete mem.ResourceAttributeProvider.GetAttributes call (direct
//     struct method, bypassing the interface) is excluded by the receiver binding
//     (methodRecvTypeName == "ResourceAttributeProvider" resolves the INTERFACE
//     method, not the concrete struct method, via go/types info.Uses), so the sole
//     path through which tenant-sharing is structural is protected.
//
// The tenant-SHARING guarantee at the sole caller is structural, not a runtime
// check: both policy load and attr fetch live inside the same scopedtx.Do closure
// bound to a single ctx-derived tid, so they share one RLS GUC transaction.
//
// # RED fixture and anti-vacuity
//
// A separate reverse self-check RED fixture was evaluated but is not possible
// from tools/archtest: ports.ResourceAttributeProvider lives in
// corecells/accesscore/INTERNAL/ports, an internal package that no package
// outside corecells/accesscore can import (compile error — Go internal-import
// visibility). This is the SAME natural upstream bound that precludes an external
// RED fixture for scopedtx.ApplyScope in tenant_applyscope_write_caller_test.go.
// The internal-package visibility IS the upstream Hard constraint: no caller
// outside corecells/accesscore can even reference the interface, so the only
// potential violators are already inside the package tree (where Go visibility
// can't further restrict calls). The production scan's anti-vacuity check
// (allowlist-entry-must-be-observed) is the enforcement substitute for the
// missing RED fixture.
//
// # Tool blind spot (charter §"强制盲区自检")
//
//   - A GetAttributes call assembled by reflection, or otherwise without a
//     compile-time reference to the interface method, is invisible — the same
//     known limit as every identifier-resolution funnel in this suite.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"
)

// resourceAttrPkgPath is the ports package that declares ResourceAttributeProvider.
const resourceAttrPkgPath = PlatformCellsModulePath + "/accesscore/internal/ports"

// resourceAttrGetAttributesMethod is the method name guarded by this invariant.
const resourceAttrGetAttributesMethod = "GetAttributes"

// resourceAttrProviderTypeName is the receiver interface name that owns GetAttributes.
// The receiver binding distinguishes the interface-method call from concrete
// struct methods (e.g. mem.ResourceAttributeProvider.GetAttributes).
const resourceAttrProviderTypeName = "ResourceAttributeProvider"

// resourceAttrGetAttributesCallerAllowlist is the sole sanctioned caller of
// ports.ResourceAttributeProvider.GetAttributes.
var resourceAttrGetAttributesCallerAllowlist = map[string]struct{}{
	"corecells/accesscore/slices/authorizationdecide/service.go": {},
}

// TestResourceAttrTenantSharing01 asserts every production reference to
// ports.ResourceAttributeProvider.GetAttributes sits within the caller allowlist,
// and that each allowlist entry is live (anti-vacuity).
func TestResourceAttrTenantSharing01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isResourceAttrGetAttributesRef(p.TypesInfo, id) {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := resourceAttrGetAttributesCallerAllowlist[rel]; !allowed {
					pos := p.Fset.Position(id.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"RESOURCE-ATTR-TENANT-SHARING-01: ports.ResourceAttributeProvider.GetAttributes is "+
								"referenced from %s, which is not the sanctioned caller. GetAttributes must only "+
								"be called inside the same scopedtx.Do block as policy loading so policy and "+
								"resource attributes share a single tenant binding (RESOURCE-ATTR-TENANT-SHARING-01). "+
								"If this IS a new sanctioned caller that co-locates both reads in one "+
								"tenant-scoped tx, add it to resourceAttrGetAttributesCallerAllowlist with rationale.",
							rel,
						),
					})
				}
			})
		}
		return d
	})

	// Anti-vacuity: each allowlist entry must be observed; a stale entry
	// (call was moved or removed) must fail CI so it cannot become a silent
	// bypass slot.
	for f := range resourceAttrGetAttributesCallerAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"RESOURCE-ATTR-TENANT-SHARING-01: allowlist entry %q is STALE — no live "+
						"ports.ResourceAttributeProvider.GetAttributes reference observed. Either the "+
						"scanner regressed or the call was removed; drop the dead allowlist entry so "+
						"it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "RESOURCE-ATTR-TENANT-SHARING-01", diags)
}

// isResourceAttrGetAttributesRef resolves an identifier USE to the
// ports.ResourceAttributeProvider.GetAttributes interface method via go/types
// (info.Uses), binding the receiver type to "ResourceAttributeProvider". The
// receiver binding is what distinguishes the interface-method call (from the
// sanctioned service.go) from any concrete struct method calls on
// mem.ResourceAttributeProvider (which share the method name but resolve to a
// different *types.Func).
func isResourceAttrGetAttributesRef(info *types.Info, id *ast.Ident) bool {
	fn, ok := info.Uses[id].(*types.Func)
	if !ok {
		return false
	}
	return fn.Name() == resourceAttrGetAttributesMethod &&
		fn.Pkg() != nil && fn.Pkg().Path() == resourceAttrPkgPath &&
		methodRecvTypeName(fn) == resourceAttrProviderTypeName
}
