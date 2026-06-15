//go:build archtest

// Package archtest — grpc_public_method_wiring_funnel_test.go
//
// INVARIANT: GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01
//
// The runtime registrar is the SINGLE source of the gRPC public-method bypass set
// (#1675). The only sanctioned production installer of the auth interceptor's
// WithPublicMethod predicate is authOptionsWithPublicMethods in
// runtime/grpc/interceptor/chain.go, which wires WithPublicMethod(reg.IsPublicMethod)
// for both the unary and stream chains (stream.go routes through that helper, so it
// holds no direct reference). Public methods are declared in the contract via
// endpoints.grpc.methods[] (public:true), derived by cellgen into
// GRPCServiceSpec.PublicMethods, and aggregated by the registrar.
//
// This archtest locks the runtime single-source in TWO dimensions:
//
//   - Dimension 1 (API ref): forbids any production reference to
//     interceptor.WithPublicMethod outside chain.go. WithPublicMethod predicates
//     compose (OR): a method is public if ANY installed predicate returns true, so
//     a composition root passing its own WithPublicMethod via Deps.AuthOptions would
//     WIDEN the public set beyond the contract-derived overlay — a latent auth
//     bypass. Allowlisting only chain.go makes the composed union a single member
//     (the registrar) in production.
//   - Dimension 2 (field write): forbids any production WRITE of the unexported
//     authConfig.publicMethod field outside auth.go (#1675 review F1). Dimension 1
//     alone misses a same-package AuthOption that sets c.publicMethod directly
//     without referencing WithPublicMethod; locking the actual state slot closes
//     that bypass. Only WithPublicMethod (auth.go) may write it.
//
// Together they make the registrar the provably-sole production source. This is the
// runtime-side sibling of the codegen-side locks (cellgen golden + contractgen
// overlay referential pre-pass + governance FMT-41) and mirrors
// GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01.
//
// # AI-robust rating (per .claude/rules/gocell/ai-robust.md)
//
// Medium — a go/types caller-allowlist typed scan, same tier and mechanism as
// GRPC-CHAIN-UNARY-INTERCEPTOR-CALLER-01. Hard-downstream is not reachable:
// WithPublicMethod is an exported function; the single-source guarantee is the
// append-last wiring (chain.go) plus this allowlist, not type sealing. Upstream
// (contract → registrar) is locked by the codegen golden + contractgen pre-pass.
//
// # Blind spots (per AI-robust §"强制盲区自检")
//
//   - A caller that obtains WithPublicMethod through a variable/parameter typed as
//     AuthOption (not a direct reference to the func object) escapes the ident scan
//     — the same alias blind spot CALLER-01 documents. The value-ref case
//     (f := WithPublicMethod; opt := f(...)) IS caught: the RHS ident resolves to
//     the func object via go/types Uses.
//   - The scan is production-only (Production excludes _test.go); tests freely use
//     WithPublicMethod to exercise the predicate.
//
// Anti-vacuity: the sole allowlisted file must host a live reference (stale-entry
// reverse check below), and TestArchtest_GRPCPublicMethodWiringFunnel01_NegativeControl
// runs the identical scan with an EMPTY allowlist and asserts the live chain.go
// reference IS flagged — proving the matcher is not vacuously green.
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

// publicMethodFieldWriteAllowlist is the set of production files permitted to
// WRITE the unexported authConfig.publicMethod field — the gRPC public-method
// bypass state slot. The sole sanctioned writer is WithPublicMethod in auth.go.
// Locking the FIELD (not just the WithPublicMethod API reference) closes the
// same-package bypass where a new in-package AuthOption assigns c.publicMethod
// directly, widening the public set without going through WithPublicMethod /
// the registrar single source (#1675 review F1).
var publicMethodFieldWriteAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/auth.go": {},
}

// isAuthConfigPublicMethodField reports whether obj is the unexported
// authConfig.publicMethod field. The field name is unique within the interceptor
// package, so name + IsField + package is precise.
func isAuthConfigPublicMethodField(obj types.Object) bool {
	v, ok := obj.(*types.Var)
	return ok && v.IsField() && v.Name() == "publicMethod" &&
		v.Pkg() != nil && v.Pkg().Path() == grpcInterceptorPkgPath
}

// scanPublicMethodFieldWrites scans production code for WRITES to
// authConfig.publicMethod — assignment LHS (c.publicMethod = ...) and composite
// literal keys (authConfig{publicMethod: ...}) — returning a diagnostic for every
// write whose file is not in allowlist, plus the observed write files.
func scanPublicMethodFieldWrites(t *testing.T, allowlist map[string]struct{}) ([]Diagnostic, map[string]struct{}) {
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		flag := func(rel string, pos token.Pos) {
			observed[rel] = struct{}{}
			if _, ok := allowlist[rel]; ok {
				return
			}
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(pos).Line,
				Message: fmt.Sprintf(
					"GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01: authConfig.publicMethod (the gRPC public-method "+
						"bypass state slot) is written from %s, which is not the sanctioned writer. Only "+
						"WithPublicMethod in runtime/grpc/interceptor/auth.go may set this field; writing it elsewhere "+
						"widens the public-method set without the registrar single source (#1675) — a latent auth "+
						"bypass the WithPublicMethod-reference scan alone would miss. Route public methods through the "+
						"contract overlay (endpoints.grpc.methods[].public:true). If this IS a new sanctioned writer, "+
						"add it to publicMethodFieldWriteAllowlist with rationale.",
					rel),
			})
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			// Form A: assignment LHS — c.publicMethod = ...
			EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
				EachInChildren[ast.SelectorExpr](as, func(sel *ast.SelectorExpr) {
					if sel.Sel.Name != "publicMethod" {
						return
					}
					if s := p.TypesInfo.Selections[sel]; s != nil && isAuthConfigPublicMethodField(s.Obj()) {
						flag(rel, sel.Pos())
					}
				})
			})
			// Form B: composite literal key — authConfig{publicMethod: ...}
			EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
				EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "publicMethod" &&
						isAuthConfigPublicMethodField(p.TypesInfo.Uses[key]) {
						flag(rel, key.Pos())
					}
				})
			})
		}
		return d
	})
	return diags, observed
}

// withPublicMethodCallerAllowlist is the set of production files permitted to
// reference interceptor.WithPublicMethod. The sole sanctioned site is
// authOptionsWithPublicMethods in chain.go, which installs
// WithPublicMethod(reg.IsPublicMethod) for both the unary and stream chains;
// stream.go routes through that helper and holds no direct reference.
var withPublicMethodCallerAllowlist = map[string]struct{}{
	"runtime/grpc/interceptor/chain.go": {},
}

// isWithPublicMethodFunc reports whether id resolves (via go/types Uses) to the
// interceptor.WithPublicMethod function object. Scanning all idents catches both
// the in-package bare-ident reference (chain.go) and any cross-package
// interceptor.WithPublicMethod selector (the .Sel ident also resolves to the func).
// The func DECLARATION's name ident is in Defs, not Uses, so it is not matched.
func isWithPublicMethodFunc(info *types.Info, id *ast.Ident) bool {
	fn, ok := info.Uses[id].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == grpcInterceptorPkgPath && fn.Name() == "WithPublicMethod"
}

// scanWithPublicMethodRefs scans production code for references to
// interceptor.WithPublicMethod, returning a diagnostic for every reference whose
// file is not in allowlist, plus the set of files where a reference was observed.
// Parameterizing the allowlist lets the negative-control test reuse the identical
// scan with an empty allowlist.
func scanWithPublicMethodRefs(t *testing.T, allowlist map[string]struct{}) ([]Diagnostic, map[string]struct{}) {
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isWithPublicMethodFunc(p.TypesInfo, id) {
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
						"GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01: interceptor.WithPublicMethod is referenced from "+
							"%s, which is not the sanctioned wiring site. The registrar is the single runtime source "+
							"of the gRPC public-method bypass set (#1675): the only sanctioned installer is "+
							"authOptionsWithPublicMethods in runtime/grpc/interceptor/chain.go, which wires "+
							"WithPublicMethod(reg.IsPublicMethod). WithPublicMethod predicates compose (OR), so a "+
							"composition root passing one via Deps.AuthOptions would WIDEN the public set beyond the "+
							"contract-derived overlay — a latent auth bypass. Declare public methods via "+
							"endpoints.grpc.methods[] (public:true). If this IS a new sanctioned wiring site, add it to "+
							"withPublicMethodCallerAllowlist with rationale.",
						rel,
					),
				})
			})
		}
		return d
	})
	return diags, observed
}

// TestArchtest_GRPCPublicMethodWiringFunnel01 asserts that every production
// reference to interceptor.WithPublicMethod sits in the caller allowlist, and that
// the sole allowlisted file actually hosts a live reference (anti-vacuity).
func TestArchtest_GRPCPublicMethodWiringFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanWithPublicMethodRefs(t, withPublicMethodCallerAllowlist)

	// Anti-vacuity / no-stale reverse self-check: the sole allowlisted file must
	// host a live reference. A stale entry is a latent bypass slot.
	for f := range withPublicMethodCallerAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01: allowlist entry %q is STALE — no live "+
						"interceptor.WithPublicMethod reference observed. The wiring site moved or the scanner "+
						"regressed; update the allowlist so a dead entry cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	// Dimension 2 (review F1): lock WRITES to the authConfig.publicMethod field, not
	// just WithPublicMethod references — a same-package AuthOption could set the slot
	// directly and bypass dimension 1.
	fieldDiags, fieldObserved := scanPublicMethodFieldWrites(t, publicMethodFieldWriteAllowlist)
	diags = append(diags, fieldDiags...)
	for f := range publicMethodFieldWriteAllowlist {
		if _, seen := fieldObserved[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01: field-write allowlist entry %q is STALE — no live "+
						"authConfig.publicMethod write observed. WithPublicMethod moved or the scanner regressed; "+
						"update the allowlist so a dead entry cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01", diags)
}

// TestArchtest_GRPCPublicMethodWiringFunnel01_NegativeControl is the synthetic red
// case: running the identical scan with an EMPTY allowlist must flag the live
// chain.go reference, proving the matcher fires on a real reference (not vacuously
// green) and that an out-of-allowlist reference produces a diagnostic.
func TestArchtest_GRPCPublicMethodWiringFunnel01_NegativeControl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags, observed := scanWithPublicMethodRefs(t, map[string]struct{}{})

	require.NotEmpty(t, diags,
		"negative control: an empty allowlist must flag the live WithPublicMethod reference")
	require.Contains(t, observed, "runtime/grpc/interceptor/chain.go",
		"negative control: chain.go must host the sanctioned WithPublicMethod reference")
	found := false
	for _, d := range diags {
		if d.Rel == "runtime/grpc/interceptor/chain.go" {
			found = true
		}
	}
	assert.True(t, found,
		"negative control: the chain.go reference must be the flagged out-of-allowlist diagnostic")

	// Dimension 2: empty allowlist must flag the live authConfig.publicMethod field
	// write in auth.go, proving the field-write scan is not vacuously green.
	fieldDiags, fieldObserved := scanPublicMethodFieldWrites(t, map[string]struct{}{})
	require.NotEmpty(t, fieldDiags,
		"negative control: an empty allowlist must flag the live authConfig.publicMethod field write")
	require.Contains(t, fieldObserved, "runtime/grpc/interceptor/auth.go",
		"negative control: auth.go must host the sanctioned authConfig.publicMethod write (WithPublicMethod)")
}
