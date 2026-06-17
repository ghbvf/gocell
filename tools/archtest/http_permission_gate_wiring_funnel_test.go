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
// resolver) against it. The resolver is the data source; the gate downstream is inert
// without one.
//
// This archtest locks the resolver SOURCE: auth.NewStaticMethodPolicyResolver may be
// referenced in production ONLY from a generated cell_gen.go. A hand-written call
// (in a slice, cell, composition root, …) would build a resolver from a hand-authored
// contractID→action map, bypassing the endpoints.http.permission overlay — a latent
// authorization-source drift that the contract-fanout closure and FMT-42 could not see.
//
// Why locking the resolver source suffices (RequirePermissionForContract is downstream):
// the cell resolver var is unexported and package-scoped to the cell, so no other
// package can obtain it; the only HTTP resolver values in production come from this
// constructor. RequirePermissionForContract takes a resolver, so a hand-wired gate still
// needs a resolver — which only the locked constructor produces. Its own wiring inside
// handler_gen.go is additionally byte-locked by the contractgen golden
// (synth_http_auth_modes_permission). (handler_gen.go lives under <module>/generated/,
// which Production scope excludes, so it is not — and need not be — scanned here.)
//
// # AI-robust rating (per .claude/rules/gocell/ai-robust.md)
//
// Medium — a go/types caller-allowlist typed scan, same tier and mechanism as
// GRPC-PERMISSION-GATE-WIRING-FUNNEL-01. The allowlist is by codegen-output filename
// (cell_gen.go), robust to where a cell lives (basename match, not a fixed path), so a
// cell move does not silently open a bypass. Hard-downstream is not reachable:
// NewStaticMethodPolicyResolver is an exported func; the single-source guarantee is the
// cellgen template (golden-locked) plus this allowlist, not type sealing.
//
// # Blind spots (per AI-robust §"强制盲区自检")
//
//   - Filename-based allowlist: a hand-written non-generated file literally named
//     cell_gen.go would be exempt. Such a file colliding with the codegen output name
//     is itself a review-visible anomaly (and would be overwritten by `gocell generate
//     cell`), so this is an accepted Medium ceiling.
//   - The scan is production + generated-excluded (Production); tests freely call the
//     constructor to exercise the resolver.
//   - A caller obtaining the func through a variable/parameter typed as a func value
//     (not a direct reference) escapes the ident scan — the same alias blind spot the
//     gRPC funnel documents.
//
// Anti-vacuity: the sanctioned cell_gen.go reference must be live (the configcore
// #2205 migration produces it; a stale-or-missing ref fails the anti-vacuity check),
// and the NegativeControl runs the identical scan treating NO file as sanctioned and
// asserts the live cell_gen.go reference IS flagged — proving the matcher is not
// vacuously green.
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

// TestArchtest_HTTPPermissionGateWiringFunnel01 asserts that every production
// reference to auth.NewStaticMethodPolicyResolver sits in a generated cell_gen.go, and
// that at least one such reference is live (anti-vacuity).
func TestArchtest_HTTPPermissionGateWiringFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanHTTPResolverCtorRefs(t, true)
	if len(observed) == 0 {
		diags = append(diags, Diagnostic{
			Message: "HTTP-PERMISSION-GATE-WIRING-FUNNEL-01: no live auth.NewStaticMethodPolicyResolver " +
				"reference observed in any generated cell_gen.go — the funnel is vacuous. The configcore #2205 " +
				"migration should produce one; if the cellgen template moved, update httpResolverCodegenFile.",
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
