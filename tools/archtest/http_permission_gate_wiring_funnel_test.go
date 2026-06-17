//go:build archtest

// Package archtest — http_permission_gate_wiring_funnel_test.go
//
// INVARIANT: HTTP-PERMISSION-GATE-WIRING-FUNNEL-01
//
// The contract `endpoints.http.permission` overlay is the SINGLE source of the HTTP
// route ABAC PDP gate (#2205) — the HTTP sibling of GRPC-PERMISSION-GATE-WIRING-FUNNEL-01.
// cellgen renders one cell-level resolver per cell from that overlay:
//
//	var cellHTTPResolver = auth.NewStaticMethodPolicyResolver(map[string]string{ <contractID>: <action>, … })
//
// in the cell's generated cell_gen.go, and the generated HTTP handlers
// (handler_gen.go) build their gate via auth.RequirePermissionForContract(contractSpec.ID,
// resolver) against it.
//
// Locking the resolver SOURCE alone does NOT suffice. authz.MethodPolicyResolver is an
// EXPORTED interface, so hand-written production code can implement its own resolver
// (never touching NewStaticMethodPolicyResolver) and pass it to the EXPORTED
// auth.RequirePermissionForContract — fabricating a contract-derived gate from a
// hand-authored permission, bypassing the endpoints.http.permission overlay entirely.
// So this funnel locks BOTH ends, mirroring GRPC-PERMISSION-GATE-WIRING-FUNNEL-01's two
// dimensions:
//
//	D1 (resolver source): auth.NewStaticMethodPolicyResolver may be referenced in
//	    production ONLY from a generated cell_gen.go (basename allowlist). Its callers
//	    live in cell_gen.go, which sits under corecells/examples (NOT generated/), so
//	    Production scope sees them — a hand-written constructor call is flagged here.
//	D2 (gate caller): auth.RequirePermissionForContract may be called ONLY from a
//	    generated handler_gen.go. Its sanctioned callers live under <module>/generated/,
//	    which Production scope EXCLUDES — so ANY caller the Production scan observes is
//	    by definition hand-written (a bypass) and flagged. The generated callers are
//	    counted by a separate Typed scan over ./generated/... purely as the anti-vacuity
//	    anchor (the funnel guards nothing if no handler ever calls the gate). Same shape
//	    as COMMAND-ASYNC-EMIT-CALLER-01's generated-caller handling.
//
// Together D1+D2 mean neither a forged resolver nor a hand-wired gate can produce a
// contract-derived route gate outside codegen. handler_gen.go's gate wiring is also
// byte-locked by the contractgen golden (synth_http_auth_modes_permission), and the
// helper itself fails fast on a nil/typed-nil resolver (validation.IsNilInterface).
//
// # AI-robust rating (per .claude/rules/gocell/ai-robust.md)
//
// Medium — go/types caller-allowlist typed scans, same tier and mechanism as
// GRPC-PERMISSION-GATE-WIRING-FUNNEL-01. Hard-downstream is not reachable: both symbols
// are exported, so the single-source guarantee is the codegen templates (golden-locked)
// plus these allowlists, not type sealing. The Hard path (a sealed generated-only
// carrier so hand-written code cannot even name a contract-derived gate) is the #2205
// review's documented 重构 option, deferred with the rest of the PR-13 hardening.
//
// # Blind spots (per AI-robust §"强制盲区自检")
//
//   - Filename-based allowlist (D1): a hand-written non-generated file literally named
//     cell_gen.go would be exempt — but it would be overwritten by `gocell generate
//     cell` and is review-visible, an accepted Medium ceiling.
//   - Both scans are production + generated-excluded (Production); tests freely use the
//     symbols. D2's generated callers are reached only by the anti-vacuity Typed scan.
//   - A caller obtaining either symbol through a variable/parameter typed as a func/
//     interface value (not a direct reference) escapes the ident scan — the same alias
//     blind spot the gRPC funnel documents.
//
// Anti-vacuity: D1's sanctioned cell_gen.go reference must be live + the NegativeControl
// flags it under an empty allowlist; D2's generated handler_gen.go gate callers must be
// live (a missing anchor = funnel vacuous). Both fail closed if the #2205 migration
// artifacts disappear.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runtimeAuthPkgPath is the framework runtime/auth import path, the home of the
// contract-derived HTTP authz primitives (#2205).
const runtimeAuthPkgPath = PlatformFrameworkModulePath + "/runtime/auth"

// httpResolverCtorName is the resolver-source constructor this funnel locks.
const httpResolverCtorName = "NewStaticMethodPolicyResolver"

// httpResolverCodegenFile is the codegen-output filename that is its SOLE sanctioned
// caller (the per-cell generated file that renders cellHTTPResolver).
const httpResolverCodegenFile = "cell_gen.go"

// httpGateFuncName is the contract-derived HTTP route gate this funnel's D2 dimension
// locks; httpGatePkgGlob is the generated subtree its sanctioned callers live under.
const (
	httpGateFuncName = "RequirePermissionForContract"
	httpGatePkgGlob  = "./generated/contracts/http/..."
)

// isHTTPGateCaller reports whether call is a call to auth.RequirePermissionForContract.
func isHTTPGateCaller(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != runtimeAuthPkgPath {
		return false
	}
	return fn.Name() == httpGateFuncName
}

// isHTTPResolverCtor reports whether id resolves (via go/types Uses) to
// auth.NewStaticMethodPolicyResolver.
func isHTTPResolverCtor(info *types.Info, id *ast.Ident) bool {
	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != runtimeAuthPkgPath {
		return false
	}
	return fn.Name() == httpResolverCtorName
}

// scanHTTPResolverCtorRefs scans production code (generated/ excluded by Production)
// for references to auth.NewStaticMethodPolicyResolver. When enforce is true a ref is
// a violation unless its file basename is the sanctioned codegen filename; when
// enforce is false (negative control) EVERY ref is reported. The second return value
// is the set of files where a reference was observed at the sanctioned codegen site.
func scanHTTPResolverCtorRefs(t *testing.T, enforce bool) ([]Diagnostic, map[string]struct{}) {
	observedAtCodegen := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			atCodegen := strings.HasSuffix(rel, "/"+httpResolverCodegenFile) || rel == httpResolverCodegenFile
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isHTTPResolverCtor(p.TypesInfo, id) {
					return
				}
				if atCodegen {
					observedAtCodegen[rel] = struct{}{}
				}
				if enforce && atCodegen {
					return
				}
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(id.Pos()).Line,
					Message: fmt.Sprintf(
						"HTTP-PERMISSION-GATE-WIRING-FUNNEL-01: auth.%s is referenced from %s, which is not "+
							"codegen output (%s). The HTTP route authz resolver has a single source: declare "+
							"endpoints.http.permission on each contract and let cellgen render the cell resolver in "+
							"cell_gen.go. Hand-building a resolver bypasses that overlay — a latent authorization-source "+
							"drift. If this IS a new sanctioned codegen template, update httpResolverCodegenFile.",
						httpResolverCtorName, rel, httpResolverCodegenFile,
					),
				})
			})
		}
		return d
	})
	return diags, observedAtCodegen
}

// scanHTTPGateCallersProduction scans production code (generated/ excluded by Production)
// for calls to auth.RequirePermissionForContract. The SOLE sanctioned callers are
// generated handler_gen.go, which live under <module>/generated/ and are therefore NOT
// in Production scope — so ANY caller this scan observes is hand-written (a bypass that
// forges a contract-derived gate without going through endpoints.http.permission) and is
// flagged. Mirrors the Production half of COMMAND-ASYNC-EMIT-CALLER-01.
func scanHTTPGateCallersProduction(t *testing.T) []Diagnostic {
	return Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		// runtime/auth OWNS the gate; its own def/internal use is not a bypass.
		if p.Pkg.Path() == runtimeAuthPkgPath {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if !isHTTPGateCaller(p.TypesInfo, call) {
					return
				}
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(call.Pos()).Line,
					Message: fmt.Sprintf(
						"HTTP-PERMISSION-GATE-WIRING-FUNNEL-01: %s calls auth.%s directly. The contract-derived "+
							"HTTP route gate may be wired ONLY by a generated handler_gen.go (from "+
							"endpoints.http.permission). A hand-written call fabricates a gate from a hand-authored "+
							"permission/resolver, bypassing the contract overlay — declare endpoints.http.permission "+
							"and regenerate instead.",
						rel, httpGateFuncName),
				})
			})
		}
		return d
	})
}

// generatedHTTPGateCallerCount counts calls to auth.RequirePermissionForContract under
// ./generated/contracts/http/... — the anti-vacuity anchor for D2. The Production scan
// above excludes generated/, so without this anchor the gate-caller funnel could guard
// nothing (no sanctioned caller ever observed). A zero count means codegen stopped
// emitting the gate (template regression) or the scanner drifted.
func generatedHTTPGateCallerCount(t *testing.T) int {
	count := 0
	_ = Run(t, Typed(TypedOpts{}, []string{httpGatePkgGlob}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if isHTTPGateCaller(p.TypesInfo, call) {
					count++
				}
			})
		}
		return nil
	})
	return count
}

// TestArchtest_HTTPPermissionGateWiringFunnel01 locks BOTH dimensions: D1 — every
// production reference to auth.NewStaticMethodPolicyResolver sits in a generated
// cell_gen.go (+ live anti-vacuity); D2 — auth.RequirePermissionForContract is never
// called from hand-written production code (its sanctioned generated callers are live).
func TestArchtest_HTTPPermissionGateWiringFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// D1: resolver source.
	diags, observed := scanHTTPResolverCtorRefs(t, true)
	if len(observed) == 0 {
		diags = append(diags, Diagnostic{
			Message: "HTTP-PERMISSION-GATE-WIRING-FUNNEL-01 (D1): no live auth.NewStaticMethodPolicyResolver " +
				"reference observed in any generated cell_gen.go — the funnel is vacuous. The configcore #2205 " +
				"migration should produce one; if the cellgen template moved, update httpResolverCodegenFile.",
		})
	}

	// D2: gate caller. Flag any hand-written caller; anchor on live generated callers.
	diags = append(diags, scanHTTPGateCallersProduction(t)...)
	if generatedHTTPGateCallerCount(t) == 0 {
		diags = append(diags, Diagnostic{
			Message: "HTTP-PERMISSION-GATE-WIRING-FUNNEL-01 (D2): no live auth.RequirePermissionForContract call " +
				"observed under " + httpGatePkgGlob + " — the gate-caller funnel is vacuous. A generated " +
				"handler_gen.go should call it (configcore #2205 migration); if the contractgen template moved, " +
				"update httpGatePkgGlob / httpGateFuncName.",
		})
	}

	Report(t, "HTTP-PERMISSION-GATE-WIRING-FUNNEL-01", diags)
}

// TestArchtest_HTTPPermissionGateWiringFunnel01_NegativeControl is the synthetic red
// case: running the identical scan treating NO file as sanctioned (enforce=false) must
// flag the live cell_gen.go reference, proving the matcher is not vacuously green.
func TestArchtest_HTTPPermissionGateWiringFunnel01_NegativeControl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanHTTPResolverCtorRefs(t, false)
	require.NotEmpty(t, diags,
		"negative control: treating no file as sanctioned must flag the live NewStaticMethodPolicyResolver reference")
	require.NotEmpty(t, observed,
		"negative control: a generated cell_gen.go must host the sanctioned resolver-source reference")
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "auth."+httpResolverCtorName) {
			found = true
			break
		}
	}
	require.True(t, found,
		"negative control: the resolver-source constructor must be individually flagged when no file is sanctioned")
}
