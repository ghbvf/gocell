//go:build archtest

// Package archtest — grpc_permission_gate_wiring_funnel_test.go
//
// INVARIANT: GRPC-PERMISSION-GATE-WIRING-FUNNEL-01
//
// The runtime registrar + composition-root Authorizer are the SINGLE source of the
// gRPC per-method ABAC PDP gate (#2008) — the authorization sibling of the #1675
// public-method bypass funnel. The only sanctioned production installer of the auth
// interceptor's WithPermissionResolver and WithPDPAuthorizer options is
// authChainOptions in runtime/grpc/interceptor/chain.go, which wires
// WithPermissionResolver(reg.PermissionForMethod) (derived from each cell's
// endpoints.grpc.methods[].permission overlay) and WithPDPAuthorizer(deps.Authorizer)
// (the cell-provided PDP, the gRPC analog of bootstrap.WithPrimaryAuthorizer) for
// both the unary and stream chains.
//
// This archtest locks the runtime single-source in TWO dimensions (mirroring
// GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01):
//
//   - Dimension 1 (API ref): forbids any production reference to
//     interceptor.WithPermissionResolver or interceptor.WithPDPAuthorizer outside
//     chain.go. WithPermissionResolver is LAST-WINS: a composition root installing
//     its own resolver via Deps.AuthOptions would OVERRIDE the contract-derived
//     registrar source — silently widening (or zeroing) the method→permission map, a
//     latent authorization bypass. WithPDPAuthorizer likewise must come only from the
//     single composition-root Authorizer. Allowlisting only chain.go keeps the
//     production source singular.
//   - Dimension 2 (field write): forbids any production WRITE of the unexported
//     authConfig.permissionFor or authConfig.authorizer fields outside auth.go.
//     Dimension 1 alone misses a same-package AuthOption that sets the slot directly
//     without referencing the option func; locking the actual state slots closes
//     that bypass. Only WithPermissionResolver / WithPDPAuthorizer (auth.go) may
//     write them.
//
// Together they make the registrar + composition-root Authorizer the provably-sole
// production source. This is the runtime-side sibling of the codegen-side locks
// (cellgen MethodPermissions golden + the contractgen/cellgen referential +
// completeness pre-pass + governance FMT-41 closed-set check).
//
// # AI-robust rating (per .claude/rules/gocell/ai-robust.md)
//
// Medium — a go/types caller-allowlist typed scan, same tier and mechanism as
// GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01. Hard-downstream is not reachable:
// WithPermissionResolver / WithPDPAuthorizer are exported funcs; the single-source
// guarantee is the append wiring (chain.go) plus this allowlist, not type sealing.
// Upstream (contract → registrar) is locked by the codegen golden + completeness
// pre-pass; the closed permission set is locked by FMT-41 + the registrar's
// fail-fast authz.PermissionByName resolution.
//
// # Blind spots (per AI-robust §"强制盲区自检")
//
//   - A caller obtaining the option func through a variable/parameter typed as
//     AuthOption (not a direct reference) escapes the ident scan — the same alias
//     blind spot CALLER-01 / the public-method funnel document.
//   - A composition root could pass an AuthOption via Deps.AuthOptions whose CLOSURE
//     BODY calls WithPermissionResolver/WithPDPAuthorizer (a wrap-and-call escaping
//     the direct-ident scan). The field-write dimension (Dimension 2) backstops this
//     for the state slots, but the API-ref dimension alone would miss it — same
//     wrap-and-call blind spot the public-method funnel carries.
//   - The scan is production-only (Production excludes _test.go); tests freely use
//     the options to exercise the gate.
//
// Anti-vacuity: each allowlisted file must host a live reference (stale-entry
// reverse check below), and the NegativeControl runs the identical scan with an
// EMPTY allowlist and asserts the live chain.go / auth.go references ARE flagged —
// proving the matcher is not vacuously green.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// permissionGateOptionFuncs are the interceptor option funcs whose production
// references this funnel restricts to chain.go.
var permissionGateOptionFuncs = []string{"WithPermissionResolver", "WithPDPAuthorizer"}

// permissionGateFields are the unexported authConfig fields whose production writes
// this funnel restricts to auth.go.
var permissionGateFields = []string{"permissionFor", "authorizer"}

// permissionGateRefAllowlist is the set of production files permitted to reference
// the permission-gate option funcs. The sole sanctioned site is authChainOptions in
// chain.go, which installs both for the unary and stream chains.
var permissionGateRefAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/chain.go": {},
}

// permissionGateFieldWriteAllowlist is the set of production files permitted to
// WRITE the unexported authConfig.permissionFor / authConfig.authorizer fields. The
// sole sanctioned writer is auth.go (WithPermissionResolver / WithPDPAuthorizer).
var permissionGateFieldWriteAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/auth.go": {},
}

// isPermissionGateOptionFunc reports whether id resolves (via go/types Uses) to one
// of the interceptor permission-gate option funcs.
func isPermissionGateOptionFunc(info *types.Info, id *ast.Ident) (string, bool) {
	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != grpcInterceptorPkgPath {
		return "", false
	}
	for _, name := range permissionGateOptionFuncs {
		if fn.Name() == name {
			return name, true
		}
	}
	return "", false
}

// isPermissionGateField reports whether obj is one of the unexported authConfig
// permission-gate fields. The field names are unique within the interceptor package.
func isPermissionGateField(obj types.Object) (string, bool) {
	v, ok := obj.(*types.Var)
	if !ok || !v.IsField() || v.Pkg() == nil || v.Pkg().Path() != grpcInterceptorPkgPath {
		return "", false
	}
	for _, name := range permissionGateFields {
		if v.Name() == name {
			return name, true
		}
	}
	return "", false
}

// scanPermissionGateRefs scans production code for references to the permission-gate
// option funcs, returning a diagnostic for every reference whose file is not in
// allowlist, plus the set of files where a reference was observed.
func scanPermissionGateRefs(t *testing.T, allowlist map[string]struct{}) ([]Diagnostic, map[string]struct{}) {
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				name, ok := isPermissionGateOptionFunc(p.TypesInfo, id)
				if !ok {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := allowlist[rel]; allowed {
					return
				}
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(id.Pos()).Line,
					Message: fmt.Sprintf(
						"GRPC-PERMISSION-GATE-WIRING-FUNNEL-01: interceptor.%s is referenced from %s, which is "+
							"not the sanctioned wiring site. The registrar (PermissionForMethod) + composition-root "+
							"Authorizer are the single runtime source of the gRPC per-method PDP gate (#2008): the only "+
							"sanctioned installer is authChainOptions in runtime/grpc/interceptor/chain.go. "+
							"WithPermissionResolver is LAST-WINS, so a resolver passed via Deps.AuthOptions would OVERRIDE "+
							"the contract-derived map — a latent authorization bypass. Declare per-method permissions via "+
							"endpoints.grpc.methods[].permission. If this IS a new sanctioned wiring site, add it to "+
							"permissionGateRefAllowlist with rationale.",
						name, rel,
					),
				})
			})
		}
		return d
	})
	return diags, observed
}

// scanPermissionGateFieldWrites scans production code for WRITES to the
// authConfig.permissionFor / authConfig.authorizer fields — assignment LHS and
// composite-literal keys — returning a diagnostic for every write whose file is not
// in allowlist, plus the observed write files.
func scanPermissionGateFieldWrites(t *testing.T, allowlist map[string]struct{}) ([]Diagnostic, map[string]struct{}) {
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		flag := func(field, rel string, pos token.Pos) {
			observed[rel] = struct{}{}
			if _, ok := allowlist[rel]; ok {
				return
			}
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(pos).Line,
				Message: fmt.Sprintf(
					"GRPC-PERMISSION-GATE-WIRING-FUNNEL-01: authConfig.%s (a gRPC PDP-gate state slot) is written "+
						"from %s, which is not the sanctioned writer. Only WithPermissionResolver / WithPDPAuthorizer in "+
						"runtime/grpc/interceptor/auth.go may set these fields; writing them elsewhere bypasses the "+
						"registrar + composition-root single source (#2008) — a latent authorization bypass the option-ref "+
						"scan alone would miss. If this IS a new sanctioned writer, add it to "+
						"permissionGateFieldWriteAllowlist with rationale.",
					field, rel,
				),
			})
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			// Form A: assignment LHS — c.permissionFor = ... / c.authorizer = ...
			EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
				EachInChildren[ast.SelectorExpr](as, func(sel *ast.SelectorExpr) {
					if !exprInList(as.Lhs, sel) {
						return
					}
					if s := p.TypesInfo.Selections[sel]; s != nil {
						if field, ok := isPermissionGateField(s.Obj()); ok {
							flag(field, rel, sel.Pos())
						}
					}
				})
			})
			// Form B: composite literal key — authConfig{permissionFor: ...}
			EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
				EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						return
					}
					if field, ok := isPermissionGateField(p.TypesInfo.Uses[key]); ok {
						flag(field, rel, key.Pos())
					}
				})
			})
		}
		return d
	})
	return diags, observed
}

// TestArchtest_GRPCPermissionGateWiringFunnel01 asserts that every production
// reference to the permission-gate option funcs sits in the caller allowlist, every
// write to the gate state slots sits in the field-write allowlist, and each
// allowlisted file actually hosts a live reference (anti-vacuity).
func TestArchtest_GRPCPermissionGateWiringFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanPermissionGateRefs(t, permissionGateRefAllowlist)
	for f := range permissionGateRefAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-PERMISSION-GATE-WIRING-FUNNEL-01: ref allowlist entry %q is STALE — no live "+
						"WithPermissionResolver/WithPDPAuthorizer reference observed. The wiring site moved or the "+
						"scanner regressed; update the allowlist so a dead entry cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	fieldDiags, fieldObserved := scanPermissionGateFieldWrites(t, permissionGateFieldWriteAllowlist)
	diags = append(diags, fieldDiags...)
	for f := range permissionGateFieldWriteAllowlist {
		if _, seen := fieldObserved[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-PERMISSION-GATE-WIRING-FUNNEL-01: field-write allowlist entry %q is STALE — no live "+
						"authConfig.permissionFor/authorizer write observed. The writer moved or the scanner regressed; "+
						"update the allowlist so a dead entry cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "GRPC-PERMISSION-GATE-WIRING-FUNNEL-01", diags)
}

// TestArchtest_GRPCPermissionGateWiringFunnel01_NegativeControl is the synthetic red
// case: running the identical scans with an EMPTY allowlist must flag the live
// chain.go references and the live auth.go field writes, proving neither matcher is
// vacuously green.
func TestArchtest_GRPCPermissionGateWiringFunnel01_NegativeControl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanPermissionGateRefs(t, map[string]struct{}{})
	require.NotEmpty(t, diags,
		"negative control: an empty allowlist must flag the live permission-gate option references")
	require.Contains(t, observed, "runtime/grpc/interceptor/chain.go",
		"negative control: chain.go must host the sanctioned option references")
	found := false
	for _, d := range diags {
		if d.Rel == "runtime/grpc/interceptor/chain.go" {
			found = true
		}
	}
	assert.True(t, found,
		"negative control: the chain.go reference must be the flagged out-of-allowlist diagnostic")

	fieldDiags, fieldObserved := scanPermissionGateFieldWrites(t, map[string]struct{}{})
	require.NotEmpty(t, fieldDiags,
		"negative control: an empty allowlist must flag the live authConfig field writes")
	require.Contains(t, fieldObserved, "runtime/grpc/interceptor/auth.go",
		"negative control: auth.go must host the sanctioned authConfig.permissionFor/authorizer writes")
}
