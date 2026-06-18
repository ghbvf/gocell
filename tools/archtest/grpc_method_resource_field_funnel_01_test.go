//go:build archtest

// Package archtest — grpc_method_resource_field_funnel_01_test.go
//
// INVARIANT: GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01
//
// The runtime registrar is the SINGLE source of the gRPC per-method resource-field
// resolver (#2207) — the third auth dimension alongside the public-method bypass
// (#1675) and the per-method permission gate (#2008). The only sanctioned production
// installer of the auth interceptor's WithResourceResolver option is authChainOptions
// in runtime/grpc/interceptor/chain.go, which wires
// WithResourceResolver(reg.ResourceFieldForMethod) (derived from each cell's
// endpoints.grpc.methods[].resource overlay) for both the unary and stream chains.
//
// This archtest locks the runtime single-source in TWO dimensions (mirroring
// GRPC-PERMISSION-GATE-WIRING-FUNNEL-01):
//
//   - Dimension 1 (API ref): forbids any production reference to
//     interceptor.WithResourceResolver outside chain.go. WithResourceResolver is
//     LAST-WINS: a composition root installing its own resolver via Deps.AuthOptions
//     would OVERRIDE the contract-derived registrar source — silently replacing the
//     per-message resource with an arbitrary value, potentially bypassing ownership
//     authz or locking out legitimate owners (F3 fail-closed means a wrong field
//     name → all requests denied). Allowlisting only chain.go keeps the production
//     source singular.
//   - Dimension 2 (field write): forbids any production WRITE of the unexported
//     authConfig.resourceFor field outside auth.go. Dimension 1 alone misses a
//     same-package AuthOption that sets the slot directly without referencing the
//     option func; locking the actual state slot closes that bypass. Only
//     WithResourceResolver (auth.go) may write it.
//
// Together they make the registrar the provably-sole production source of
// WithResourceResolver. This is the runtime-side sibling of the codegen-side lock:
// cellgen derives MethodResources from endpoints.grpc.methods[].resource; the
// cellgen cross-check (#2207 Hard) catches owner-scoped permissions without a
// resource selector at build time; this archtest enforces the runtime wiring at
// Medium.
//
// # AI-robust rating (per .claude/rules/gocell/ai-robust.md)
//
// Medium — a go/types caller-allowlist typed scan, same tier and mechanism as
// GRPC-PERMISSION-GATE-WIRING-FUNNEL-01. Hard-downstream is not reachable:
// WithResourceResolver is an exported func; the single-source guarantee is the
// append wiring (chain.go) plus this allowlist, not type sealing. Upstream
// (contract → registrar) is locked by the cellgen golden + cross-check + governance
// FMT-41.
//
// # Blind spots (per AI-robust §"强制盲区自检")
//
//   - A caller obtaining the option func through a variable/parameter typed as
//     AuthOption (not a direct reference) escapes the ident scan — the same alias
//     blind spot GRPC-PERMISSION-GATE-WIRING-FUNNEL-01 documents.
//   - A composition root passing an AuthOption via Deps.AuthOptions whose CLOSURE
//     BODY calls WithResourceResolver (wrap-and-call) escapes the direct-ident scan.
//     The field-write dimension (Dimension 2) backstops this for the state slot.
//   - The scan is production-only (Production excludes _test.go); tests freely use
//     the option to exercise resource extraction.
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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resourceResolverOptionFuncs are the interceptor option funcs whose production
// references this funnel restricts to chain.go.
var resourceResolverOptionFuncs = []string{"WithResourceResolver"}

// resourceResolverFields are the unexported authConfig fields whose production
// writes this funnel restricts to auth.go.
var resourceResolverFields = []string{"resourceFor"}

// resourceResolverRefAllowlist is the set of production files permitted to reference
// the resource-resolver option func. The sole sanctioned site is authChainOptions in
// chain.go, which installs it for both the unary and stream chains.
var resourceResolverRefAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/chain.go": {},
}

// resourceResolverFieldWriteAllowlist is the set of production files permitted to
// WRITE the unexported authConfig.resourceFor field. The sole sanctioned writer is
// auth.go (WithResourceResolver).
var resourceResolverFieldWriteAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/auth.go": {},
}

// isResourceResolverOptionFunc reports whether id resolves (via go/types Uses) to
// interceptor.WithResourceResolver.
func isResourceResolverOptionFunc(info *types.Info, id *ast.Ident) (string, bool) {
	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != grpcInterceptorPkgPath {
		return "", false
	}
	for _, name := range resourceResolverOptionFuncs {
		if fn.Name() == name {
			return name, true
		}
	}
	return "", false
}

// isResourceResolverField reports whether obj is one of the unexported authConfig
// resource-resolver fields.
func isResourceResolverField(obj types.Object) (string, bool) {
	v, ok := obj.(*types.Var)
	if !ok || !v.IsField() || v.Pkg() == nil || v.Pkg().Path() != grpcInterceptorPkgPath {
		return "", false
	}
	for _, name := range resourceResolverFields {
		if v.Name() == name {
			return name, true
		}
	}
	return "", false
}

// scanResourceResolverRefs scans production code for references to the resource-
// resolver option func, returning a diagnostic for every reference whose file is not
// in allowlist, plus the set of files where a reference was observed.
func scanResourceResolverRefs(t *testing.T, allowlist map[string]struct{}) ([]Diagnostic, map[string]struct{}) {
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				name, ok := isResourceResolverOptionFunc(p.TypesInfo, id)
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
						"GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01: interceptor.%s is referenced from %s, which is "+
							"not the sanctioned wiring site. The registrar (ResourceFieldForMethod) is the single runtime "+
							"source of the gRPC per-message resource-field resolver (#2207): the only sanctioned installer is "+
							"authChainOptions in runtime/grpc/interceptor/chain.go. WithResourceResolver is LAST-WINS, so a "+
							"resolver passed via Deps.AuthOptions would OVERRIDE the contract-derived map — silently replacing "+
							"the per-message resource, potentially bypassing ownership authz or locking out legitimate owners "+
							"(F3 fail-closed: wrong field name → all requests denied). Declare per-method resource fields via "+
							"endpoints.grpc.methods[].resource. If this IS a new sanctioned wiring site, add it to "+
							"resourceResolverRefAllowlist with rationale.",
						name, rel,
					),
				})
			})
		}
		return d
	})
	return diags, observed
}

// scanResourceResolverFieldWrites scans production code for WRITES to the
// authConfig.resourceFor field — assignment LHS and composite-literal keys —
// returning a diagnostic for every write whose file is not in allowlist, plus the
// observed write files.
func scanResourceResolverFieldWrites(t *testing.T, allowlist map[string]struct{}) ([]Diagnostic, map[string]struct{}) {
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
					"GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01: authConfig.%s (the gRPC resource-field resolver state slot) is "+
						"written from %s, which is not the sanctioned writer. Only WithResourceResolver in "+
						"runtime/grpc/interceptor/auth.go may set this field; writing it elsewhere bypasses the registrar "+
						"single source (#2207) — a latent ownership bypass or lock-out the option-ref scan alone would miss. "+
						"If this IS a new sanctioned writer, add it to resourceResolverFieldWriteAllowlist with rationale.",
					field, rel,
				),
			})
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			// Form A: assignment LHS — c.resourceFor = ...
			EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
				EachInChildren[ast.SelectorExpr](as, func(sel *ast.SelectorExpr) {
					if !exprInList(as.Lhs, sel) {
						return
					}
					if s := p.TypesInfo.Selections[sel]; s != nil {
						if field, ok := isResourceResolverField(s.Obj()); ok {
							flag(field, rel, sel.Pos())
						}
					}
				})
			})
			// Form B: composite literal key — authConfig{resourceFor: ...}
			EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
				EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						return
					}
					if field, ok := isResourceResolverField(p.TypesInfo.Uses[key]); ok {
						flag(field, rel, key.Pos())
					}
				})
			})
		}
		return d
	})
	return diags, observed
}

// TestArchtest_GRPCMethodResourceFieldFunnel01 asserts that every production
// reference to the resource-resolver option func sits in the caller allowlist,
// every write to the gate state slot sits in the field-write allowlist, and each
// allowlisted file actually hosts a live reference (anti-vacuity).
func TestArchtest_GRPCMethodResourceFieldFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanResourceResolverRefs(t, resourceResolverRefAllowlist)
	for f := range resourceResolverRefAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01: ref allowlist entry %q is STALE — no live "+
						"WithResourceResolver reference observed. The wiring site moved or the scanner regressed; "+
						"update the allowlist so a dead entry cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	fieldDiags, fieldObserved := scanResourceResolverFieldWrites(t, resourceResolverFieldWriteAllowlist)
	diags = append(diags, fieldDiags...)
	for f := range resourceResolverFieldWriteAllowlist {
		if _, seen := fieldObserved[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01: field-write allowlist entry %q is STALE — no live "+
						"authConfig.resourceFor write observed. The writer moved or the scanner regressed; "+
						"update the allowlist so a dead entry cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01", diags)
}

// TestArchtest_GRPCMethodResourceFieldFunnel01_NegativeControl is the synthetic red
// case: running the identical scans with an EMPTY allowlist must flag the live
// chain.go references and the live auth.go field writes, proving neither matcher is
// vacuously green.
//
// Per-symbol anti-vacuity: every name in resourceResolverOptionFuncs must appear in at
// least one ref diagnostic, and every name in resourceResolverFields must appear in at
// least one field-write diagnostic.
func TestArchtest_GRPCMethodResourceFieldFunnel01_NegativeControl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Dimension 1: option-func references (chain.go).
	diags, observed := scanResourceResolverRefs(t, map[string]struct{}{})
	require.NotEmpty(t, diags,
		"negative control: an empty allowlist must flag the live resource-resolver option references")
	require.Contains(t, observed, "runtime/grpc/interceptor/chain.go",
		"negative control: chain.go must host the sanctioned option reference")

	for _, name := range resourceResolverOptionFuncs {
		name := name
		found := false
		for _, d := range diags {
			if strings.Contains(d.Message, "interceptor."+name) {
				found = true
				break
			}
		}
		assert.True(t, found,
			"negative control: option func %q must be individually flagged when allowlist is empty", name)
	}

	// Dimension 2: field writes (auth.go).
	fieldDiags, fieldObserved := scanResourceResolverFieldWrites(t, map[string]struct{}{})
	require.NotEmpty(t, fieldDiags,
		"negative control: an empty allowlist must flag the live authConfig.resourceFor writes")
	require.Contains(t, fieldObserved, "runtime/grpc/interceptor/auth.go",
		"negative control: auth.go must host the sanctioned authConfig.resourceFor writes")

	for _, field := range resourceResolverFields {
		field := field
		found := false
		for _, d := range fieldDiags {
			if strings.Contains(d.Message, "authConfig."+field) {
				found = true
				break
			}
		}
		assert.True(t, found,
			"negative control: field %q must be individually flagged when allowlist is empty", field)
	}
}
